package alerting

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/notificationtemplate"
	"github.com/neko233-com/MetricsPanel233/internal/receivers"
	"github.com/neko233-com/MetricsPanel233/internal/store"
)

// Native ownership remains an internal fixture-only mode until atomic legacy
// reconciliation/cutover and webhook preparation are implemented. Execution
// shares the same parent claim and RunNotifications scheduler.
func (e *Engine) processReceiverNotification(ctx context.Context, delivery model.NotificationDelivery, at time.Time) error {
	source, err := e.Store.ReceiverNotificationSource(ctx, delivery, notificationTime(at))
	if err != nil {
		return e.finishReceiverPreparationError(ctx, delivery, at, err)
	}
	prepared, err := e.Store.ReceiverSourcePrepared(ctx, source)
	if err != nil {
		return err
	}
	if !prepared {
		destinations, err := e.receiverSourceDestinations(ctx, delivery, source)
		if err != nil {
			return e.finishReceiverPreparationError(ctx, delivery, at, err)
		}
		parts, err := e.Store.ReceiverNotificationPartitions(ctx, source, notificationTime(at), destinations)
		if err != nil {
			return e.finishReceiverPreparationError(ctx, delivery, at, err)
		}
		inputs := make([]model.ReceiverDispatchInput, 0, len(parts))
		for index, part := range parts {
			input, err := e.prepareReceiverPartition(ctx, delivery, part, index)
			if err != nil {
				return e.finishReceiverPreparationError(ctx, delivery, at, err)
			}
			inputs = append(inputs, input)
		}
		if err = e.Store.EnqueueReceiverSourceDispatches(ctx, source, inputs, notificationTime(at)); err != nil {
			return e.finishReceiverPreparationError(ctx, delivery, at, err)
		}
	}
	// Keep a source's immutable bytes after transient transport failures. Each
	// target's own bounded attempts decide retries; parent claims do not consume a
	// second independent retry budget.
	for range 1024 {
		claim, err := e.Store.ClaimReceiverSourceDelivery(ctx, source, notificationTime(at))
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return e.finishReceiverPreparationError(ctx, delivery, at, err)
		}
		_, sendErr := e.Store.SendClaimedReceiverDelivery(ctx, claim)
		claim.Close()
		if sendErr != nil {
			// Stop after a failed checkpoint; the durable claim determines what
			// may be replayed after its lease expires.
			return e.finishReceiverPreparationError(ctx, delivery, at, sendErr)
		}
	}
	return e.Store.FinishReceiverSourceNotification(ctx, source, notificationTime(at))
}

func notificationTime(at time.Time) time.Time { return maxTime(time.Now(), at) }
func maxTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return b
	}
	return a
}

func (e *Engine) finishReceiverPreparationError(ctx context.Context, delivery model.NotificationDelivery, at time.Time, cause error) error {
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if errors.Is(cause, store.ErrReceiverLease) {
		return cause
	}
	if errors.Is(cause, store.ErrReceiverSourceChanged) || errors.Is(cause, store.ErrReceiverSuppressed) {
		valid, err := e.Store.ValidateNotificationClaim(finishCtx, delivery, notificationTime(at).UnixMilli())
		if err != nil {
			return err
		}
		if !valid {
			return nil
		}
		return e.Store.InvalidateReceiverNotificationSource(finishCtx, delivery, notificationTime(at))
	}
	if errors.Is(cause, store.ErrReceiverSourceUnsupported) || errors.Is(cause, errSavedTemplateRender) {
		return e.Store.FinishNotification(finishCtx, delivery, "failed", "Cannot prepare saved receiver source", 0, 0, notificationTime(at).UnixMilli())
	}
	// Retain a source after DB or ACK-checkpoint failure; do not guess whether
	// the last remote operation succeeded or transmit another target.
	return cause
}

func (e *Engine) prepareReceiverPartition(ctx context.Context, delivery model.NotificationDelivery, part store.ReceiverNotificationPartition, index int) (model.ReceiverDispatchInput, error) {
	var payload map[string]any
	decoder := json.NewDecoder(bytes.NewReader(part.Payload))
	decoder.UseNumber()
	if decoder.Decode(&payload) != nil {
		return model.ReceiverDispatchInput{}, errSavedTemplateRender
	}
	payload["notificationId"] = strconv.FormatInt(delivery.ID, 10)
	if e.TemplateURL != nil {
		payload["externalURL"] = e.TemplateURL.String()
	}
	data, err := notificationtemplate.DataFromPayload(payload, time.UnixMilli(delivery.CreatedAt))
	if err != nil {
		return model.ReceiverDispatchInput{}, errSavedTemplateRender
	}
	for i := range data.Alerts {
		if i < len(part.Periods) {
			data.Alerts[i].RuleUID = part.Periods[i].RuleUID
		}
	}
	compiled, err := notificationtemplate.Compile(part.Definitions)
	if err != nil {
		return model.ReceiverDispatchInput{}, errSavedTemplateRender
	}
	renderCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	render := func(expression, fallback string) (string, error) {
		if expression == "" {
			expression = fallback
		}
		value, err := compiled.Render(renderCtx, expression, data)
		if err != nil {
			return "", errors.Join(errSavedTemplateRender, err)
		}
		return value, nil
	}
	meta := receivers.Metadata{DeliveryID: fmt.Sprintf("notification:%d:partition:%d", delivery.ID, index), CreatedAt: time.UnixMilli(delivery.CreatedAt)}
	var prepared receivers.Prepared
	switch part.Integration.Type {
	case "email":
		var settings receivers.EmailSettings
		if json.Unmarshal(part.Integration.Settings, &settings) != nil {
			return model.ReceiverDispatchInput{}, errSavedTemplateRender
		}
		subject, err := render(settings.Subject, `{{ template "default.title" . }}`)
		if err != nil {
			return model.ReceiverDispatchInput{}, err
		}
		message, err := render(settings.Message, `{{ template "default.message" . }}`)
		if err != nil {
			return model.ReceiverDispatchInput{}, err
		}
		prepared, err = e.Store.PrepareReceiverPartitionEmail(ctx, part, receivers.EmailContent{Subject: subject, TextBody: message}, meta)
		if err != nil {
			return model.ReceiverDispatchInput{}, err
		}
	case "slack":
		var settings receivers.SlackSettings
		if json.Unmarshal(part.Integration.Settings, &settings) != nil {
			return model.ReceiverDispatchInput{}, errSavedTemplateRender
		}
		content := receivers.SlackContent{MentionChannel: settings.MentionChannel, MentionUsers: settings.MentionUsers, MentionGroups: settings.MentionGroups}
		for _, field := range []struct {
			expression, fallback string
			target               *string
		}{
			{settings.Recipient, "", &content.Recipient}, {settings.Text, `{{ template "default.message" . }}`, &content.Text}, {settings.Title, `{{ template "default.title" . }}`, &content.Title}, {settings.Username, "", &content.Username}, {settings.IconEmoji, "", &content.IconEmoji}, {settings.IconURL, "", &content.IconURL}, {settings.Color, "", &content.Color}, {settings.Footer, "", &content.Footer},
		} {
			if field.expression == "" && field.fallback == "" {
				continue
			}
			value, renderErr := render(field.expression, field.fallback)
			if renderErr != nil {
				return model.ReceiverDispatchInput{}, renderErr
			}
			*field.target = value
		}
		content.Recipient = part.Target
		prepared, err = e.Store.PrepareReceiverSlack(ctx, part.Integration.UID, part.Integration.Revision, content, meta)
		if err != nil {
			return model.ReceiverDispatchInput{}, err
		}
	default:
		return model.ReceiverDispatchInput{}, store.ErrReceiverSourceUnsupported
	}
	periods := map[string][]model.ReceiverPeriod{}
	members := map[string][]model.ReceiverMember{}
	for _, op := range prepared.Operations() {
		if part.Status != "test" {
			periods[op.Key] = append([]model.ReceiverPeriod{}, part.Periods...)
			members[op.Key] = append([]model.ReceiverMember{}, part.Members...)
		}
	}
	if part.Status == "test" {
		members = nil
	}
	return model.ReceiverDispatchInput{Status: part.Status, DataSnapshot: part.Payload, TemplateSnapshot: mustReceiverJSON(part.Definitions), Deliveries: []model.ReceiverPreparedDelivery{{IntegrationUID: part.Integration.UID, Revision: part.Integration.Revision, Prepared: prepared, Periods: periods, Members: members}}}, nil
}

func (e *Engine) receiverSourceDestinations(ctx context.Context, delivery model.NotificationDelivery, source *store.ReceiverNotificationSource) (map[string][]string, error) {
	plans, err := e.Store.ReceiverNotificationFiringPlans(source)
	if err != nil {
		return nil, err
	}
	destinations := map[string][]string{}
	for _, plan := range plans {
		switch plan.Integration.Type {
		case "email":
			var settings receivers.EmailSettings
			if json.Unmarshal(plan.Integration.Settings, &settings) != nil {
				return nil, errSavedTemplateRender
			}
			targets, err := receivers.EmailRecipients(settings)
			if err != nil {
				return nil, err
			}
			destinations[plan.Integration.UID] = targets
		case "slack":
			var settings receivers.SlackSettings
			if json.Unmarshal(plan.Integration.Settings, &settings) != nil {
				return nil, errSavedTemplateRender
			}
			var payload map[string]any
			decoder := json.NewDecoder(bytes.NewReader(plan.Payload))
			decoder.UseNumber()
			if decoder.Decode(&payload) != nil {
				return nil, errSavedTemplateRender
			}
			data, err := notificationtemplate.DataFromPayload(payload, time.UnixMilli(delivery.CreatedAt))
			if err != nil {
				return nil, errSavedTemplateRender
			}
			compiled, err := notificationtemplate.Compile(plan.Definitions)
			if err != nil {
				return nil, errSavedTemplateRender
			}
			renderCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			target, err := compiled.Render(renderCtx, settings.Recipient, data)
			cancel()
			if err != nil {
				return nil, errors.Join(errSavedTemplateRender, err)
			}
			destinations[plan.Integration.UID] = []string{target}
		default:
			return nil, store.ErrReceiverSourceUnsupported
		}
	}
	return destinations, nil
}

func mustReceiverJSON(value any) json.RawMessage { raw, _ := json.Marshal(value); return raw }

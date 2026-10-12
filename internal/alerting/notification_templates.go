package alerting

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/notificationtemplate"
)

var errSavedTemplateRender = errors.New("notification template rendering failed for the saved version")

func (e *Engine) prepareTemplateNotification(ctx context.Context, delivery model.NotificationDelivery, raw json.RawMessage) (json.RawMessage, bool, error) {
	snapshot, exists, err := e.Store.NotificationRenderSnapshot(ctx, delivery.ID)
	if err != nil || !exists {
		return raw, false, err
	}
	if snapshot.Error != "" {
		return nil, true, errSavedTemplateRender
	}
	if len(snapshot.Payload) > 0 {
		return snapshot.Payload, true, nil
	}
	const failure = "Notification template rendering failed for the saved version"
	payload := map[string]any{}
	renderErr := json.Unmarshal(raw, &payload)
	if renderErr == nil {
		payload["notificationId"] = strconv.FormatInt(delivery.ID, 10)
		if e.TemplateURL != nil {
			payload["externalURL"] = e.TemplateURL.String()
			if alerts, ok := payload["alerts"].([]any); ok {
				for _, item := range alerts {
					if alert, ok := item.(map[string]any); ok {
						alert["generatorURL"] = e.TemplateURL.String() + "/#alerts"
					}
				}
			}
		}
		data, err := notificationtemplate.DataFromPayload(payload, time.UnixMilli(delivery.CreatedAt))
		renderErr = err
		if renderErr == nil {
			rules, err := e.Store.NotificationRenderRules(ctx, delivery.ID, delivery.RuleUID)
			if err != nil {
				return nil, true, err
			}
			for i := range data.Alerts {
				if i < len(rules) {
					data.Alerts[i].RuleUID = rules[i]
				}
			}
			compiled, err := notificationtemplate.Compile(snapshot.Bundle.Templates)
			if renderErr == nil {
				renderErr = err
			}
			if renderErr == nil {
				renderCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
				defer cancel()
				for _, field := range []struct{ key, expression string }{{"title", snapshot.Bundle.Title}, {"message", snapshot.Bundle.Message}} {
					if field.expression == "" {
						continue
					}
					value, err := compiled.Render(renderCtx, field.expression, data)
					if err != nil {
						renderErr = err
						break
					}
					payload[field.key] = value
				}
			}
		}
	}
	if renderErr != nil {
		_, message, err := e.Store.SaveNotificationRender(ctx, delivery, nil, failure)
		if err != nil {
			return nil, true, err
		}
		return nil, true, errors.Join(errSavedTemplateRender, errors.New(message))
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, true, err
	}
	saved, message, err := e.Store.SaveNotificationRender(ctx, delivery, encoded, "")
	if err != nil {
		return nil, true, err
	}
	if message != "" {
		return nil, true, errors.Join(errSavedTemplateRender, errors.New(message))
	}
	return saved, true, nil
}

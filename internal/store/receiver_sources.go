package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/notificationtemplate"
	"github.com/neko233-com/MetricsPanel233/internal/receivers"
)

var ErrReceiverSourceChanged = errors.New("notification source ownership or current alert membership changed")
var ErrReceiverSourceUnsupported = errors.New("notification source requires unsupported legacy receiver reconciliation")

type NotificationQueueOwnership struct {
	Mode  string `json:"mode"`
	Epoch int64  `json:"epoch"`
}

type receiverProducerContextKey struct{}
type receiverPendingGroupSourceContextKey struct{}

func receiverProducerContext(ctx context.Context, s *Store) context.Context {
	return context.WithValue(ctx, receiverProducerContextKey{}, s)
}

func notificationQueueOwnershipTx(ctx context.Context, tx *sql.Tx) (NotificationQueueOwnership, error) {
	owner := NotificationQueueOwnership{Mode: "legacy", Epoch: 1}
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='alert_receiver_ownership'`).Scan(&exists); err != nil {
		return owner, err
	}
	if exists == 0 {
		return owner, nil
	}
	err := tx.QueryRowContext(ctx, `SELECT mode,epoch FROM alert_receiver_ownership WHERE id=1`).Scan(&owner.Mode, &owner.Epoch)
	if err == nil && (owner.Mode != "legacy" && owner.Mode != "native" || owner.Epoch < 1) {
		err = ErrReceiverSourceChanged
	}
	return owner, err
}
func (s *Store) NotificationQueueOwnership(ctx context.Context) (NotificationQueueOwnership, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return NotificationQueueOwnership{}, err
	}
	defer tx.Rollback()
	return notificationQueueOwnershipTx(ctx, tx)
}

type receiverSourceMember struct {
	Period model.ReceiverPeriod `json:"period"`
	Status string               `json:"status"`
	Alert  json.RawMessage      `json:"alert"`
}
type receiverSourceSnapshot struct {
	Receiver    model.Receiver                    `json:"receiver"`
	Parent      model.NotificationDelivery        `json:"parent"`
	Owner       NotificationQueueOwnership        `json:"owner"`
	GroupKey    string                            `json:"group_key"`
	Generation  int64                             `json:"generation"`
	Payload     json.RawMessage                   `json:"payload"`
	Definitions []notificationtemplate.Definition `json:"definitions"`
	Members     []receiverSourceMember            `json:"members"`
	Unsupported bool                              `json:"unsupported"`
}

// ReceiverNotificationSource is an internal, immutable preparation capability.
// JSON only reveals the safe identity, never alert data, expressions or payloads.
type ReceiverNotificationSource struct {
	ID              int64  `json:"id"`
	ReceiverUID     string `json:"receiver_uid"`
	ReceiverVersion int64  `json:"receiver_version"`
	OwnerEpoch      int64  `json:"owner_epoch"`
	Fingerprint     string `json:"fingerprint"`
	snapshot        receiverSourceSnapshot
	lease           model.NotificationDelivery
	store           *Store
}

func (s *Store) receiverSourceTx(ctx context.Context, tx *sql.Tx, id int64) (*ReceiverNotificationSource, error) {
	v := &ReceiverNotificationSource{ID: id, store: s}
	var sealed []byte
	err := tx.QueryRowContext(ctx, `SELECT receiver_uid,receiver_version,owner_epoch,fingerprint,sealed FROM alert_receiver_sources WHERE notification_id=?`, id).Scan(&v.ReceiverUID, &v.ReceiverVersion, &v.OwnerEpoch, &v.Fingerprint, &sealed)
	if err != nil {
		return nil, err
	}
	if err = s.openReceiver("notification-source", fmt.Sprint(id), v.OwnerEpoch, sealed, &v.snapshot); err != nil {
		return nil, err
	}
	if v.snapshot.Parent.ID != id || v.snapshot.Receiver.UID != v.ReceiverUID || v.snapshot.Receiver.Version != v.ReceiverVersion || v.snapshot.Owner.Epoch != v.OwnerEpoch || s.receiverHash("notification-source", v.snapshot) != v.Fingerprint {
		return nil, ErrReceiverSnapshot
	}
	return v, nil
}

// Called in the producer's alert-state transaction. A grouped producer calls it
// once more AFTER inserting group membership, before either snapshot is visible.
func (s *Store) snapshotReceiverNotificationSourceTx(ctx context.Context, tx *sql.Tx, id int64) error {
	return freezeReceiverNotificationSourceTx(ctx, tx, s, id)
}
func snapshotReceiverNotificationSourceTx(ctx context.Context, tx *sql.Tx, id int64) error {
	if s, ok := ctx.Value(receiverProducerContextKey{}).(*Store); ok {
		return freezeReceiverNotificationSourceTx(ctx, tx, s, id)
	}
	owner, err := notificationQueueOwnershipTx(ctx, tx)
	if err != nil {
		return err
	}
	if owner.Mode == "native" {
		return ErrReceiverSourceChanged
	}
	return nil
}
func freezeReceiverNotificationSourceTx(ctx context.Context, tx *sql.Tx, s *Store, id int64) error {
	if deferred, _ := ctx.Value(receiverPendingGroupSourceContextKey{}).(bool); deferred {
		return nil
	}
	organizationID, err := notificationOrganizationTx(ctx, tx)
	if err != nil {
		return err
	}
	if organizationID != s.OrganizationID() {
		return ErrReceiverSourceChanged
	}
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='alert_receiver_sources'`).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return nil
	}
	parent, err := scanNotification(tx.QueryRowContext(ctx, `SELECT `+notificationColumns+` FROM alert_notification_outbox WHERE id=?`, id))
	if err != nil {
		return err
	}
	owner, err := notificationQueueOwnershipTx(ctx, tx)
	if err != nil {
		return err
	}
	receiver, err := receiverTx(ctx, tx, parent.ContactPointUID)
	if errors.Is(err, sql.ErrNoRows) {
		if owner.Mode == "native" {
			return ErrReceiverSourceChanged
		}
		return nil
	}
	if err != nil {
		return err
	}
	source := receiverSourceSnapshot{Receiver: receiver, Parent: parent, Owner: owner, Definitions: []notificationtemplate.Definition{}, Members: []receiverSourceMember{}}
	for _, i := range receiver.Integrations {
		if i.Disabled {
			continue
		}
		if i.Type != "email" && i.Type != "slack" {
			source.Unsupported = true
		}
		// Never consume the one-time migration's stale contact point configuration.
		if i.ConfigVersion == "legacy" {
			old, e := s.integrationSnapshotTx(ctx, tx, i.UID, i.Revision)
			if e != nil {
				return e
			}
			point, e := scanContactPoint(tx.QueryRowContext(ctx, `SELECT config FROM alert_contact_points WHERE uid=?`, parent.ContactPointUID))
			if e != nil {
				return e
			}
			if old.LegacyPoint == nil || old.LegacyPoint.Version != point.Version {
				source.Unsupported = true
			}
		}
	}
	var raw string
	if err = tx.QueryRowContext(ctx, `SELECT payload FROM alert_notification_outbox WHERE id=?`, id).Scan(&raw); err != nil {
		return err
	}
	source.Payload = json.RawMessage(raw)
	definitions, err := notificationTemplatesTx(ctx, tx)
	if err != nil {
		return err
	}
	source.Definitions = templateDefinitions(definitions)
	var payload struct {
		Alerts []json.RawMessage `json:"alerts"`
	}
	if json.Unmarshal(source.Payload, &payload) != nil {
		return ErrReceiverSnapshot
	}
	var groupRaw string
	err = tx.QueryRowContext(ctx, `SELECT group_key,generation,members FROM alert_notification_group_deliveries WHERE notification_id=?`, id).Scan(&source.GroupKey, &source.Generation, &groupRaw)
	if errors.Is(err, sql.ErrNoRows) {
		source.GroupKey = fmt.Sprintf("event:%s:%s", parent.RuleUID, parent.InstanceKey)
		source.Generation = 0
		for _, alert := range payload.Alerts {
			var a struct {
				Status   string    `json:"status"`
				StartsAt time.Time `json:"startsAt"`
			}
			if json.Unmarshal(alert, &a) != nil {
				return ErrReceiverSnapshot
			}
			source.Members = append(source.Members, receiverSourceMember{Period: model.ReceiverPeriod{RuleUID: parent.RuleUID, InstanceKey: parent.InstanceKey, StartedAt: a.StartsAt.UnixMilli()}, Status: a.Status, Alert: alert})
		}
	} else if err != nil {
		return err
	} else {
		var members []groupMember
		if json.Unmarshal([]byte(groupRaw), &members) != nil || len(members) != len(payload.Alerts) {
			return ErrReceiverSnapshot
		}
		for i, m := range members {
			source.Members = append(source.Members, receiverSourceMember{Period: model.ReceiverPeriod{RuleUID: m.Rule, InstanceKey: m.Key, StartedAt: m.StartedAt}, Status: m.Status, Alert: payload.Alerts[i]})
		}
	}
	if len(source.Members) == 0 || len(source.Members) > 1000 || len(source.Payload) > 1<<20 {
		return ErrReceiverSnapshot
	}
	seen := map[model.ReceiverPeriod]bool{}
	for _, member := range source.Members {
		var alert struct {
			Status   string    `json:"status"`
			StartsAt time.Time `json:"startsAt"`
		}
		if json.Unmarshal(member.Alert, &alert) != nil || alert.Status != member.Status || alert.StartsAt.UnixMilli() != member.Period.StartedAt || member.Status != "firing" && member.Status != "resolved" || parent.Status != "test" && !member.Period.Valid() || seen[member.Period] {
			return ErrReceiverSnapshot
		}
		seen[member.Period] = true
	}
	fingerprint := s.receiverHash("notification-source", source)
	sealed, err := s.sealReceiver("notification-source", fmt.Sprint(id), owner.Epoch, source)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO alert_receiver_sources(notification_id,receiver_uid,receiver_version,owner_epoch,group_key,generation,status,fingerprint,sealed) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(notification_id) DO UPDATE SET group_key=excluded.group_key,generation=excluded.generation,status=excluded.status,fingerprint=excluded.fingerprint,sealed=excluded.sealed WHERE EXISTS(SELECT 1 FROM alert_notification_outbox n WHERE n.id=excluded.notification_id AND n.state='pending' AND n.attempts=0)`, id, receiver.UID, receiver.Version, owner.Epoch, source.GroupKey, source.Generation, parent.Status, fingerprint, sealed)
	return err
}

// ReceiverResolvedTarget is a trusted preparation plan: only targets that
// actually ACKed the supplied periods appear. No credentials or body is exposed.
type ReceiverResolvedTarget struct {
	IntegrationUID         string
	Revision, ChannelEpoch int64
	Protocol, Target       string
	Periods                []model.ReceiverPeriod
}

func (s *Store) ReceiverResolvedTargets(ctx context.Context, uid string, periods []model.ReceiverPeriod) ([]ReceiverResolvedTarget, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	return s.receiverResolvedTargetsTx(ctx, tx, uid, periods)
}
func (s *Store) receiverResolvedTargetsTx(ctx context.Context, tx *sql.Tx, uid string, periods []model.ReceiverPeriod) ([]ReceiverResolvedTarget, error) {
	if len(periods) < 1 || len(periods) > 1000 {
		return nil, model.ErrInvalidReceiver
	}
	current, err := receiverTx(ctx, tx, uid)
	if err != nil {
		return nil, err
	}
	enabled := map[string]bool{}
	for _, i := range current.Integrations {
		enabled[i.UID] = !i.Disabled && !i.DisableResolveMessage && (i.Type == "email" || i.Type == "slack")
	}
	type receipt struct {
		uid, key, target          string
		revision, epoch, delivery int64
	}
	plans := map[string]*ReceiverResolvedTarget{}
	for _, p := range periods {
		if !p.Valid() {
			return nil, model.ErrInvalidReceiver
		}
		rows, err := tx.QueryContext(ctx, `SELECT f.integration_uid,d.revision,f.channel_epoch,f.delivery_id,f.op_key,f.target_key FROM alert_receiver_firing_receipts f JOIN alert_receiver_deliveries d ON d.id=f.delivery_id JOIN alert_receiver_dispatches x ON x.id=d.dispatch_id WHERE x.receiver_uid=? AND x.status='firing' AND f.period_key=? ORDER BY f.integration_uid,d.revision,f.target_key`, uid, s.receiverPeriodKey(p))
		if err != nil {
			return nil, err
		}
		items := []receipt{}
		for rows.Next() {
			var r receipt
			if err = rows.Scan(&r.uid, &r.revision, &r.epoch, &r.delivery, &r.key, &r.target); err != nil {
				break
			}
			items = append(items, r)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return nil, err
		}
		for _, r := range items {
			if !enabled[r.uid] {
				continue
			}
			var sealed []byte
			if err = tx.QueryRowContext(ctx, `SELECT sealed FROM alert_receiver_deliveries WHERE id=?`, r.delivery).Scan(&sealed); err != nil {
				return nil, err
			}
			var saved receiverPreparedSnapshot
			if err = s.openReceiver("delivery", fmt.Sprint(r.delivery), r.revision, sealed, &saved); err != nil {
				return nil, err
			}
			foundPeriod := false
			for _, member := range saved.Periods[r.key] {
				if member == p {
					foundPeriod = true
				}
			}
			if !foundPeriod {
				return nil, ErrReceiverSnapshot
			}
			target := ""
			found := false
			for _, op := range saved.Prepared.Operations() {
				if op.Key == r.key {
					target = op.Target
					found = true
					break
				}
			}
			if !found || s.receiverTargetKey(saved.Prepared.Protocol(), target) != r.target {
				return nil, ErrReceiverSnapshot
			}
			key := fmt.Sprintf("%s/%d/%d/%s", r.uid, r.revision, r.epoch, r.target)
			plan := plans[key]
			if plan == nil {
				plan = &ReceiverResolvedTarget{IntegrationUID: r.uid, Revision: r.revision, ChannelEpoch: r.epoch, Protocol: saved.Prepared.Protocol(), Target: target}
				plans[key] = plan
			}
			duplicate := false
			for _, existing := range plan.Periods {
				if existing == p {
					duplicate = true
				}
			}
			if !duplicate {
				plan.Periods = append(plan.Periods, p)
			}
		}
	}
	keys := []string{}
	for key := range plans {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := []ReceiverResolvedTarget{}
	for _, key := range keys {
		result = append(result, *plans[key])
	}
	return result, nil
}
func (s *Store) receiverAnyFiringACKTx(ctx context.Context, tx *sql.Tx, uid string, p model.ReceiverPeriod) (bool, error) {
	var found int
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM alert_receiver_firing_receipts f JOIN alert_receiver_deliveries d ON d.id=f.delivery_id JOIN alert_receiver_dispatches x ON x.id=d.dispatch_id WHERE x.receiver_uid=? AND f.period_key=?)`, uid, s.receiverPeriodKey(p)).Scan(&found)
	return found != 0, err
}

// ReceiverNotificationPartition contains only members eligible for one frozen
// channel and destination. Resolution membership is selected before rendering.
// It is an internal capability; JSON cannot reveal alert or template data.
type ReceiverNotificationPartition struct {
	Integration  model.ReceiverIntegration         `json:"-"`
	Status       string                            `json:"status"`
	Target       string                            `json:"-"`
	Periods      []model.ReceiverPeriod            `json:"-"`
	Members      []model.ReceiverMember            `json:"-"`
	Payload      json.RawMessage                   `json:"-"`
	Definitions  []notificationtemplate.Definition `json:"-"`
	source       *ReceiverNotificationSource
	ackRevisions map[model.ReceiverPeriod]int64
}

func (s *Store) ReceiverNotificationSource(ctx context.Context, claim model.NotificationDelivery, at time.Time) (*ReceiverNotificationSource, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	source, err := s.receiverSourceTx(ctx, tx, claim.ID)
	if err != nil {
		return nil, err
	}
	source.lease = claim
	if err = s.validateReceiverSourceTx(ctx, tx, source, at.UnixMilli()); err != nil {
		return nil, err
	}
	return source, nil
}

func (s *Store) validateReceiverSourceTx(ctx context.Context, tx *sql.Tx, source *ReceiverNotificationSource, at int64) error {
	if source == nil || source.store != s || source.ID != source.lease.ID || at < 1 {
		return ErrReceiverLease
	}
	owner, err := notificationQueueOwnershipTx(ctx, tx)
	if err != nil {
		return err
	}
	if owner.Mode != "native" || owner != source.snapshot.Owner {
		return ErrReceiverSourceChanged
	}
	var count int
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM alert_receiver_sources src JOIN alert_notification_outbox n ON n.id=src.notification_id WHERE src.notification_id=? AND src.fingerprint=? AND src.owner_epoch=? AND n.state='sending' AND n.attempts=? AND n.last_attempt_at=? AND n.next_attempt_at>? AND n.contact_point_uid=? AND n.status=?`, source.ID, source.Fingerprint, source.OwnerEpoch, source.lease.Attempts, source.lease.LastAttemptAt, at, source.ReceiverUID, source.snapshot.Parent.Status).Scan(&count)
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrReceiverLease
	}
	if source.snapshot.Unsupported {
		return ErrReceiverSourceUnsupported
	}
	if source.snapshot.Parent.Status == "test" {
		return nil
	}
	silences, err := activeSilencesTx(ctx, tx, at)
	if err != nil {
		return err
	}
	suppressor, err := newNotificationSuppressor(ctx, tx, at, silences)
	if err != nil {
		return err
	}
	if source.snapshot.Generation > 0 {
		err = tx.QueryRowContext(ctx, `SELECT count(*) FROM alert_notification_groups g JOIN alert_notification_group_deliveries d ON d.group_key=g.group_key WHERE d.notification_id=? AND d.generation=? AND g.generation=d.generation AND g.inflight_id=d.notification_id`, source.ID, source.snapshot.Generation).Scan(&count)
		if err != nil {
			return err
		}
		if count != 1 {
			return ErrReceiverSourceChanged
		}
		_, valid, err := validGroupNotificationTx(ctx, tx, source.ID, at, suppressor)
		if err != nil {
			return err
		}
		if !valid {
			return ErrReceiverSourceChanged
		}
	}
	for _, member := range source.snapshot.Members {
		if member.Status != "firing" && member.Status != "resolved" || !member.Period.Valid() {
			return ErrReceiverSnapshot
		}
		if member.Status == "firing" {
			err = tx.QueryRowContext(ctx, `SELECT count(*) FROM alert_notification_active WHERE rule_uid=? AND instance_key=? AND started_at=?`, member.Period.RuleUID, member.Period.InstanceKey, member.Period.StartedAt).Scan(&count)
			if err != nil {
				return err
			}
			if count != 1 {
				return ErrReceiverSourceChanged
			}
		}
		var alert struct {
			Labels map[string]string `json:"labels"`
		}
		if json.Unmarshal(member.Alert, &alert) != nil {
			return ErrReceiverSnapshot
		}
		labels, err := currentNotificationLabelsTx(ctx, tx, member.Period.RuleUID, member.Period.InstanceKey, string(marshalReceiver(map[string]any{"commonLabels": alert.Labels})))
		if err != nil {
			return err
		}
		until, _, err := suppressor.check(labels)
		if err != nil {
			return err
		}
		if until != 0 {
			return ErrReceiverSourceChanged
		}
	}
	return nil
}

// Firing plans permit only destination expressions to be rendered first. The
// final alert/template body is rendered after exact ACK destination selection.
func (s *Store) ReceiverNotificationFiringPlans(source *ReceiverNotificationSource) ([]ReceiverNotificationPartition, error) {
	if source == nil || source.store != s {
		return nil, ErrReceiverSnapshot
	}
	periods := []model.ReceiverPeriod{}
	for _, member := range source.snapshot.Members {
		if member.Status == "firing" {
			periods = append(periods, member.Period)
		}
	}
	if len(periods) == 0 {
		return []ReceiverNotificationPartition{}, nil
	}
	status := "firing"
	if source.snapshot.Parent.Status == "test" {
		status = "test"
	}
	payload, err := source.partitionPayload(status, periods)
	if err != nil {
		return nil, err
	}
	result := []ReceiverNotificationPartition{}
	for _, integration := range source.snapshot.Receiver.Integrations {
		if integration.Disabled {
			continue
		}
		var clone model.ReceiverIntegration
		if json.Unmarshal(marshalReceiver(integration), &clone) != nil {
			return nil, ErrReceiverSnapshot
		}
		result = append(result, ReceiverNotificationPartition{Integration: clone, Status: status, Periods: append([]model.ReceiverPeriod{}, periods...), Payload: append(json.RawMessage{}, payload...), Definitions: append([]notificationtemplate.Definition{}, source.snapshot.Definitions...), source: source})
	}
	return result, nil
}

func (s *Store) ReceiverNotificationPartitions(ctx context.Context, source *ReceiverNotificationSource, at time.Time, destinations map[string][]string) ([]ReceiverNotificationPartition, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err = s.validateReceiverSourceTx(ctx, tx, source, at.UnixMilli()); err != nil {
		return nil, err
	}
	firing, resolved := []model.ReceiverPeriod{}, []model.ReceiverPeriod{}
	for _, member := range source.snapshot.Members {
		if member.Status == "resolved" {
			resolved = append(resolved, member.Period)
		} else {
			firing = append(firing, member.Period)
		}
	}
	plans := map[string]*ReceiverNotificationPartition{}
	key := func(i model.ReceiverIntegration, target string) string {
		return string(marshalReceiver([]any{i.UID, i.Revision, i.ChannelEpoch, target}))
	}
	for _, i := range source.snapshot.Receiver.Integrations {
		if i.Disabled {
			continue
		}
		if len(firing) > 0 {
			status := "firing"
			if source.snapshot.Parent.Status == "test" {
				status = "test"
			}
			targets := destinations[i.UID]
			if len(targets) < 1 || len(targets) > receivers.MaxOperations {
				return nil, model.ErrInvalidReceiver
			}
			for _, target := range targets {
				planKey := key(i, target)
				if plans[planKey] != nil {
					return nil, model.ErrInvalidReceiver
				}
				plans[planKey] = &ReceiverNotificationPartition{Integration: i, Status: status, Target: target, Periods: append([]model.ReceiverPeriod{}, firing...), source: source}
			}
		}
	}
	if len(resolved) > 0 {
		resolvedPlans, err := s.receiverResolvedTargetsTx(ctx, tx, source.ReceiverUID, resolved)
		if err != nil {
			return nil, err
		}
		for _, plan := range resolvedPlans {
			config, err := s.integrationSnapshotTx(ctx, tx, plan.IntegrationUID, plan.Revision)
			if err != nil {
				return nil, err
			}
			historical := config.Integration
			frozenEnabled := false
			for _, integration := range source.snapshot.Receiver.Integrations {
				if integration.UID != historical.UID || integration.Disabled || integration.DisableResolveMessage {
					continue
				}
				frozenEnabled = true
				if len(firing) > 0 && plans[key(integration, plan.Target)] != nil {
					equivalent, err := s.receiverSourceChannelEquivalentTx(ctx, tx, integration, historical)
					if err != nil {
						return nil, err
					}
					if equivalent {
						config.Integration = integration
					}
				}
				break
			}
			if !frozenEnabled {
				continue
			}
			planKey := key(config.Integration, plan.Target)
			partition := plans[planKey]
			if partition == nil {
				partition = &ReceiverNotificationPartition{Integration: config.Integration, Status: "resolved", Target: plan.Target, source: source}
				plans[planKey] = partition
			}
			partition.Periods = append(partition.Periods, plan.Periods...)
			if partition.ackRevisions == nil {
				partition.ackRevisions = map[model.ReceiverPeriod]int64{}
			}
			for _, p := range plan.Periods {
				partition.ackRevisions[p] = plan.Revision
			}
		}
	}
	keys := []string{}
	for key := range plans {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := []ReceiverNotificationPartition{}
	emailCohorts := map[string]int{}
	for _, key := range keys {
		part := plans[key]
		selected := map[model.ReceiverPeriod]bool{}
		for _, p := range part.Periods {
			selected[p] = true
		}
		part.Periods = []model.ReceiverPeriod{}
		for _, member := range source.snapshot.Members {
			if selected[member.Period] {
				part.Periods = append(part.Periods, member.Period)
				if part.Status != "test" {
					part.Members = append(part.Members, model.ReceiverMember{Period: member.Period, Status: member.Status, ACKRevision: part.ackRevisions[member.Period]})
				}
			}
		}
		part.Payload, err = source.partitionPayload(part.Status, part.Periods)
		if err != nil {
			return nil, err
		}
		part.Definitions = append([]notificationtemplate.Definition{}, source.snapshot.Definitions...)
		var clone model.ReceiverIntegration
		if json.Unmarshal(marshalReceiver(part.Integration), &clone) != nil {
			return nil, ErrReceiverSnapshot
		}
		part.Integration = clone
		// Identical membership may retain SingleEmail/BCC's shared MIME. A
		// destination with a different ACK audience belongs to another cohort,
		// so it can never appear in that MIME's body or recipient headers.
		if part.Integration.Type == "email" {
			cohort := string(marshalReceiver([]any{part.Integration.UID, part.Integration.Revision, part.Status, part.Members}))
			if index, exists := emailCohorts[cohort]; exists {
				result[index].Target = strings.Join([]string{result[index].Target, part.Target}, ";")
				continue
			}
			emailCohorts[cohort] = len(result)
		}
		result = append(result, *part)
	}
	if len(result) > 1024 {
		return nil, model.ErrInvalidReceiver
	}
	return result, nil
}

// Content-only revisions may share one mixed notification without losing the
// original firing ACK revision. Different transport configuration keeps its
// historical channel; immutable profile/credential revisions remain exact.
func (s *Store) receiverSourceChannelEquivalentTx(ctx context.Context, tx *sql.Tx, a, b model.ReceiverIntegration) (bool, error) {
	if a.UID != b.UID || a.Type != b.Type || a.ChannelEpoch != b.ChannelEpoch {
		return false, nil
	}
	if a.Type == "email" {
		return a.SMTPProfileUID == b.SMTPProfileUID && a.SMTPProfileRevision == b.SMTPProfileRevision, nil
	}
	if a.Type != "slack" {
		return false, nil
	}
	channel := func(integration model.ReceiverIntegration) (string, error) {
		snapshot, err := s.integrationSnapshotTx(ctx, tx, integration.UID, integration.Revision)
		if err != nil {
			return "", err
		}
		var settings slackIntegrationSettings
		if err = strictReceiverJSON(snapshot.Integration.Settings, &settings); err != nil {
			return "", err
		}
		return s.receiverHash("source-channel", []any{settings.EndpointURL, settings.TimeoutSeconds, settings.TLS, snapshot.Secrets}), nil
	}
	left, err := channel(a)
	if err != nil {
		return false, err
	}
	right, err := channel(b)
	return left == right, err
}

func (source *ReceiverNotificationSource) partitionPayload(status string, periods []model.ReceiverPeriod) (json.RawMessage, error) {
	var payload map[string]any
	decoder := json.NewDecoder(bytes.NewReader(source.snapshot.Payload))
	decoder.UseNumber()
	if decoder.Decode(&payload) != nil {
		return nil, ErrReceiverSnapshot
	}
	selected := map[model.ReceiverPeriod]bool{}
	for _, p := range periods {
		selected[p] = true
	}
	alerts := []json.RawMessage{}
	var commonLabels, commonAnnotations map[string]string
	for _, member := range source.snapshot.Members {
		if !selected[member.Period] {
			continue
		}
		alerts = append(alerts, member.Alert)
		var a struct{ Labels, Annotations map[string]string }
		if json.Unmarshal(member.Alert, &a) != nil {
			return nil, ErrReceiverSnapshot
		}
		intersect := func(current *map[string]string, values map[string]string) {
			if *current == nil {
				*current = map[string]string{}
				for k, v := range values {
					(*current)[k] = v
				}
				return
			}
			for k, v := range *current {
				if candidate, ok := values[k]; !ok || candidate != v {
					delete(*current, k)
				}
			}
		}
		intersect(&commonLabels, a.Labels)
		intersect(&commonAnnotations, a.Annotations)
	}
	if len(alerts) != len(selected) {
		return nil, ErrReceiverSnapshot
	}
	payload["alerts"], payload["status"] = alerts, status
	payload["receiver"] = source.snapshot.Receiver.Name
	payload["state"] = "ok"
	if status == "firing" || status == "test" {
		payload["state"] = "alerting"
	}
	payload["commonLabels"], payload["commonAnnotations"] = commonLabels, commonAnnotations
	groups := map[string]string{}
	if old, ok := payload["groupLabels"].(map[string]any); ok {
		for k := range old {
			if v, ok := commonLabels[k]; ok {
				groups[k] = v
			}
		}
	}
	payload["groupLabels"] = groups
	delete(payload, "title")
	delete(payload, "message")
	return json.Marshal(payload)
}

// PrepareReceiverPartitionEmail constrains resolved recipients before MIME is
// constructed, including SingleEmail and BCC headers.
func (s *Store) PrepareReceiverPartitionEmail(ctx context.Context, part ReceiverNotificationPartition, content receivers.EmailContent, meta receivers.Metadata) (receivers.Prepared, error) {
	if part.source == nil || part.source.store != s {
		return receivers.Prepared{}, ErrReceiverSnapshot
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return receivers.Prepared{}, err
	}
	defer tx.Rollback()
	config, err := s.integrationSnapshotTx(ctx, tx, part.Integration.UID, part.Integration.Revision)
	if err != nil {
		return receivers.Prepared{}, err
	}
	profile, err := s.profileSnapshotTx(ctx, tx, config.Integration.SMTPProfileUID, config.Integration.SMTPProfileRevision)
	if err != nil {
		return receivers.Prepared{}, err
	}
	transport, err := smtpTransport(profile)
	if err != nil {
		return receivers.Prepared{}, err
	}
	var settings receivers.EmailSettings
	if err = strictReceiverJSON(config.Integration.Settings, &settings); err != nil {
		return receivers.Prepared{}, err
	}
	settings.Addresses = part.Target
	return transport.Prepare(settings, content, meta)
}

// Source enqueue and linkage share the producer ownership/parent-lease CAS.
// An empty preparation is recorded too, so an unacknowledged resolution cannot
// acquire a different audience on a later retry.
func (s *Store) EnqueueReceiverSourceDispatches(ctx context.Context, source *ReceiverNotificationSource, inputs []model.ReceiverDispatchInput, at time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s.validateReceiverSourceTx(ctx, tx, source, at.UnixMilli()); err != nil {
		return err
	}
	var fingerprint string
	err = tx.QueryRowContext(ctx, `SELECT fingerprint FROM alert_receiver_source_preparations WHERE notification_id=?`, source.ID).Scan(&fingerprint)
	if err == nil {
		if fingerprint != source.Fingerprint {
			return ErrReceiverSnapshot
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if len(inputs) > 1024 {
		return model.ErrInvalidReceiver
	}
	for index, input := range inputs {
		if len(input.Deliveries) != 1 || input.Status != "firing" && input.Status != "resolved" && input.Status != "test" {
			return model.ErrInvalidReceiver
		}
		delivery := input.Deliveries[0]
		var expectedPeriods []model.ReceiverPeriod
		for _, operation := range delivery.Prepared.Operations() {
			periods := delivery.Periods[operation.Key]
			if input.Status == "test" {
				if source.snapshot.Parent.Status != "test" || len(periods) != 0 {
					return model.ErrInvalidReceiver
				}
				for _, member := range source.snapshot.Members {
					expectedPeriods = append(expectedPeriods, member.Period)
				}
				break
			}
			if expectedPeriods == nil {
				expectedPeriods = periods
			}
			if string(marshalReceiver(expectedPeriods)) != string(marshalReceiver(periods)) {
				return model.ErrInvalidReceiver
			}
			if len(delivery.Members[operation.Key]) != len(periods) {
				return model.ErrInvalidReceiver
			}
			for _, typedMember := range delivery.Members[operation.Key] {
				if typedMember.Status == "resolved" && typedMember.ACKRevision < 1 {
					return model.ErrInvalidReceiver
				}
				found := false
				for _, member := range source.snapshot.Members {
					if member.Period == typedMember.Period && member.Status == typedMember.Status {
						found = true
						break
					}
				}
				if !found {
					return ErrReceiverSnapshot
				}
			}
		}
		expectedPayload, err := source.partitionPayload(input.Status, expectedPeriods)
		if err != nil {
			return err
		}
		if string(expectedPayload) != string(input.DataSnapshot) || string(marshalReceiver(source.snapshot.Definitions)) != string(input.TemplateSnapshot) {
			return ErrReceiverSnapshot
		}
		input.ReceiverUID, input.ReceiverVersion = source.ReceiverUID, source.ReceiverVersion
		input.GroupKey, input.Generation = fmt.Sprintf("source:%d:%d", source.ID, index), 1
		input.CreatedAt = source.snapshot.Parent.CreatedAt
		dispatch, err := s.enqueueReceiverDispatchTx(ctx, tx, input, &source.snapshot.Receiver)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO alert_receiver_source_dispatches(notification_id,dispatch_id) VALUES(?,?)`, source.ID, dispatch.ID); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO alert_receiver_source_preparations VALUES(?,?)`, source.ID, source.Fingerprint); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ReceiverSourcePrepared(ctx context.Context, source *ReceiverNotificationSource) (bool, error) {
	if source == nil || source.store != s {
		return false, ErrReceiverSnapshot
	}
	var count int
	err := s.DB.QueryRowContext(ctx, `SELECT count(*) FROM alert_receiver_source_preparations WHERE notification_id=? AND fingerprint=?`, source.ID, source.Fingerprint).Scan(&count)
	return count == 1, err
}

func (s *Store) ClaimReceiverSourceDelivery(ctx context.Context, source *ReceiverNotificationSource, at time.Time) (*ClaimedReceiverDelivery, error) {
	return s.claimReceiverDelivery(ctx, at, 40*time.Second, source)
}

func (s *Store) FinishReceiverSourceNotification(ctx context.Context, source *ReceiverNotificationSource, at time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s.validateReceiverSourceTx(ctx, tx, source, at.UnixMilli()); err != nil {
		return err
	}
	var prepared int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM alert_receiver_source_preparations WHERE notification_id=? AND fingerprint=?`, source.ID, source.Fingerprint).Scan(&prepared); err != nil {
		return err
	}
	if prepared != 1 {
		return ErrReceiverSnapshot
	}
	var unfinished, failed, canceled, sent int
	var next int64
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(sum(d.state IN ('pending','partial','sending')),0),COALESCE(sum(d.state='failed'),0),COALESCE(sum(d.state='canceled'),0),COALESCE(sum(d.state='sent'),0),COALESCE(min(CASE WHEN d.state='sending' THEN d.lease_until WHEN d.state IN ('pending','partial') THEN d.next_attempt_at END),0) FROM alert_receiver_source_dispatches l JOIN alert_receiver_deliveries d ON d.dispatch_id=l.dispatch_id WHERE l.notification_id=?`, source.ID).Scan(&unfinished, &failed, &canceled, &sent, &next)
	if err != nil {
		return err
	}
	state, message, delivered := "sent", "", at.UnixMilli()
	if unfinished > 0 {
		state, message, delivered = "pending", "Receiver operations pending", 0
		next = max(next, at.Add(time.Second).UnixMilli())
	} else if failed > 0 {
		state, message, delivered = "failed", "Receiver operations failed", 0
	} else if canceled > 0 || sent == 0 {
		state, message, delivered = "canceled", "No eligible receiver operations", 0
	}
	if state != "pending" {
		next = 0
	}
	_, err = tx.ExecContext(ctx, `UPDATE alert_notification_outbox SET state=?,last_error=?,next_attempt_at=?,delivered_at=?,attempts=CASE WHEN ?='pending' THEN attempts-1 ELSE attempts END WHERE id=? AND state='sending' AND attempts=? AND last_attempt_at=?`, state, message, next, delivered, state, source.ID, source.lease.Attempts, source.lease.LastAttemptAt)
	if err != nil {
		return err
	}
	if err = finishGroupNotificationTx(ctx, tx, source.ID, state, at.UnixMilli()); err != nil {
		return err
	}
	return tx.Commit()
}

// A terminal parent revokes its children's execution capabilities. Preserve
// committed sent operations/receipts while draining only unfinished work.
func (s *Store) reconcileReceiverSourceTerminalTx(ctx context.Context, tx *sql.Tx, at int64) error {
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='alert_receiver_source_dispatches'`).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT d.id FROM alert_receiver_source_dispatches l JOIN alert_notification_outbox n ON n.id=l.notification_id JOIN alert_receiver_deliveries d ON d.dispatch_id=l.dispatch_id WHERE n.state IN ('failed','canceled') AND d.state IN ('pending','partial','sending')`)
	if err != nil {
		return err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err = tx.ExecContext(ctx, `UPDATE alert_receiver_operations SET state='canceled',code='source_canceled' WHERE delivery_id=? AND state IN ('pending','sending')`, id); err != nil {
			return err
		}
		if err = s.finishReceiverAggregateTx(ctx, tx, id, at); err != nil {
			return err
		}
	}
	return nil
}

// Revoke a prepared source under its exact parent claim and requeue current
// group membership. For example, disabling resolved messages must regenerate a
// firing-only source instead of discarding the group's live firing members.
func (s *Store) InvalidateReceiverNotificationSource(ctx context.Context, claim model.NotificationDelivery, at time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	source, err := s.receiverSourceTx(ctx, tx, claim.ID)
	if err != nil {
		return err
	}
	if source.snapshot.Parent.ContactPointUID != claim.ContactPointUID {
		return ErrReceiverLease
	}
	result, err := tx.ExecContext(ctx, `UPDATE alert_notification_outbox SET state='canceled',last_error='Receiver source superseded by current alert or ownership state',next_attempt_at=0 WHERE id=? AND state='sending' AND attempts=? AND last_attempt_at=? AND next_attempt_at>?`, claim.ID, claim.Attempts, claim.LastAttemptAt, at.UnixMilli())
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrReceiverLease
	}
	if source.snapshot.Generation > 0 {
		_, err = tx.ExecContext(ctx, `UPDATE alert_notification_groups SET inflight_id=0,dirty=1,generation=generation+1,next_at=? WHERE group_key=? AND inflight_id=?`, at.UnixMilli(), source.snapshot.GroupKey, source.ID)
		if err != nil {
			return err
		}
	}
	if err = s.reconcileReceiverSourceTerminalTx(ctx, tx, at.UnixMilli()); err != nil {
		return err
	}
	return tx.Commit()
}

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/receivers"
)

type receiverPreparedSnapshot struct {
	Prepared receivers.Prepared                `json:"prepared"`
	Periods  map[string][]model.ReceiverPeriod `json:"periods"`
}
type receiverDispatchSnapshot struct {
	ReceiverVersion int64 `json:"receiver_version"`
	Data, Templates json.RawMessage
	Deliveries      []model.ReceiverPreparedDelivery
}

// ClaimedReceiverDelivery exposes safe metadata only when JSON encoded. Secrets
// and prepared bytes are private and can only be consumed by a trusted worker.
type ClaimedReceiverDelivery struct {
	model.ReceiverDelivery
	token       string
	prepared    receivers.Prepared
	periods     map[string][]model.ReceiverPeriod
	claimedKeys map[string]bool
	progress    receivers.Progress
	transport   receivers.JournaledTransport
}

func (c *ClaimedReceiverDelivery) Transport() receivers.JournaledTransport { return c.transport }
func (c *ClaimedReceiverDelivery) Prepared() receivers.Prepared {
	p, _ := receivers.NewPrepared(c.prepared.Protocol(), c.prepared.Operations())
	return p
}
func (c *ClaimedReceiverDelivery) Progress() receivers.Progress {
	p := receivers.Progress{}
	for k, v := range c.progress {
		p[k] = v
	}
	return p
}
func (c *ClaimedReceiverDelivery) Close() {
	if v, ok := c.transport.(interface{ Close() }); ok {
		v.Close()
	}
}

// PrepareReceiverEmail and PrepareReceiverSlack deliberately avoid credential
// getters. They prepare against a specific immutable integration/profile revision.
func (s *Store) PrepareReceiverEmail(ctx context.Context, uid string, revision int64, content receivers.EmailContent, meta receivers.Metadata) (receivers.Prepared, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return receivers.Prepared{}, err
	}
	defer tx.Rollback()
	config, err := s.integrationSnapshotTx(ctx, tx, uid, revision)
	if err != nil {
		return receivers.Prepared{}, err
	}
	if config.Integration.Type != "email" {
		return receivers.Prepared{}, model.ErrInvalidReceiver
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
	return transport.Prepare(settings, content, meta)
}
func (s *Store) PrepareReceiverSlack(ctx context.Context, uid string, revision int64, content receivers.SlackContent, meta receivers.Metadata) (receivers.Prepared, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return receivers.Prepared{}, err
	}
	defer tx.Rollback()
	config, err := s.integrationSnapshotTx(ctx, tx, uid, revision)
	if err != nil {
		return receivers.Prepared{}, err
	}
	if config.Integration.Type != "slack" {
		return receivers.Prepared{}, model.ErrInvalidReceiver
	}
	transport, err := slackTransport(config)
	if err != nil {
		return receivers.Prepared{}, err
	}
	defer transport.Close()
	return transport.Prepare(content, meta)
}

func normalizeReceiverPrepared(d model.ReceiverPreparedDelivery, status string) (model.ReceiverPreparedDelivery, error) {
	if d.Revision < 1 || d.IntegrationUID == "" {
		return d, model.ErrInvalidReceiver
	}
	prepared, err := receivers.NewPrepared(d.Prepared.Protocol(), d.Prepared.Operations())
	if err != nil {
		return d, err
	}
	d.Prepared = prepared
	operations := prepared.Operations()
	seen := map[string]bool{}
	total := 0
	periods := map[string][]model.ReceiverPeriod{}
	for _, op := range operations {
		seen[op.Key] = true
		input := append([]model.ReceiverPeriod{}, d.Periods[op.Key]...)
		if status != "test" && len(input) == 0 {
			return d, model.ErrInvalidReceiver
		}
		sort.Slice(input, func(i, j int) bool {
			a, b := input[i], input[j]
			if a.RuleUID != b.RuleUID {
				return a.RuleUID < b.RuleUID
			}
			if a.InstanceKey != b.InstanceKey {
				return a.InstanceKey < b.InstanceKey
			}
			return a.StartedAt < b.StartedAt
		})
		for i, p := range input {
			if !p.Valid() || (i > 0 && p == input[i-1]) {
				return d, model.ErrInvalidReceiver
			}
		}
		total += len(input)
		if total > 2048 {
			return d, model.ErrInvalidReceiver
		}
		periods[op.Key] = input
	}
	for k := range d.Periods {
		if !seen[k] {
			return d, model.ErrInvalidReceiver
		}
	}
	d.Periods = periods
	return d, nil
}
func (s *Store) receiverTargetKey(protocol, target string) string {
	return s.receiverHash("operation-target", []string{protocol, target})
}
func (s *Store) receiverPeriodKey(p model.ReceiverPeriod) string {
	return s.receiverHash("firing-period", p)
}

func (s *Store) EnqueueReceiverDispatch(ctx context.Context, input model.ReceiverDispatchInput) (model.ReceiverDispatch, error) {
	if input.ReceiverUID == "" || input.ReceiverVersion < 1 || input.Generation < 1 || input.GroupKey == "" || len(input.GroupKey) > 1024 || strings.ContainsRune(input.GroupKey, 0) || len(input.Deliveries) < 1 || len(input.Deliveries) > 32 || (input.Status != "firing" && input.Status != "resolved" && input.Status != "test") || len(input.DataSnapshot)+len(input.TemplateSnapshot) > 2*receivers.MaxPayloadBytes {
		return model.ReceiverDispatch{}, model.ErrInvalidReceiver
	}
	if input.DataSnapshot == nil {
		input.DataSnapshot = json.RawMessage(`{}`)
	}
	if input.TemplateSnapshot == nil {
		input.TemplateSnapshot = json.RawMessage(`{}`)
	}
	if !json.Valid(input.DataSnapshot) || !json.Valid(input.TemplateSnapshot) {
		return model.ReceiverDispatch{}, model.ErrInvalidReceiver
	}
	if input.CreatedAt == 0 {
		input.CreatedAt = time.Now().UnixMilli()
	}
	if input.CreatedAt < 1 {
		return model.ReceiverDispatch{}, model.ErrInvalidReceiver
	}
	seen := map[string]bool{}
	totalPayload := 0
	periodCount, periodBytes := 0, 0
	input.Deliveries = append([]model.ReceiverPreparedDelivery{}, input.Deliveries...)
	for i, d := range input.Deliveries {
		if seen[d.IntegrationUID] {
			return model.ReceiverDispatch{}, model.ErrInvalidReceiver
		}
		seen[d.IntegrationUID] = true
		normalized, err := normalizeReceiverPrepared(d, input.Status)
		if err != nil {
			return model.ReceiverDispatch{}, err
		}
		input.Deliveries[i] = normalized
		periodBytes += len(marshalReceiver(normalized.Periods))
		for _, periods := range normalized.Periods {
			periodCount += len(periods)
		}
		for _, op := range d.Prepared.Operations() {
			totalPayload += len(op.Payload)
		}
	}
	if totalPayload > 8*receivers.MaxPayloadBytes || periodCount > 8192 || periodBytes > 2*receivers.MaxPayloadBytes {
		return model.ReceiverDispatch{}, model.ErrInvalidReceiver
	}
	sort.Slice(input.Deliveries, func(i, j int) bool { return input.Deliveries[i].IntegrationUID < input.Deliveries[j].IntegrationUID })
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.ReceiverDispatch{}, err
	}
	defer tx.Rollback()
	// HMAC covers the actual frozen bundle, not creation time. Retry with the
	// same generation remains idempotent even after a configuration edit.
	freeze := receiverDispatchSnapshot{ReceiverVersion: input.ReceiverVersion, Data: input.DataSnapshot, Templates: input.TemplateSnapshot, Deliveries: input.Deliveries}
	fingerprint := s.receiverHash("dispatch-fingerprint", []any{input.ReceiverUID, input.GroupKey, input.Generation, input.Status, freeze})
	var existing model.ReceiverDispatch
	err = tx.QueryRowContext(ctx, `SELECT id,receiver_uid,group_key,generation,fingerprint,status,state,created_at FROM alert_receiver_dispatches WHERE receiver_uid=? AND group_key=? AND generation=?`, input.ReceiverUID, input.GroupKey, input.Generation).Scan(&existing.ID, &existing.ReceiverUID, &existing.GroupKey, &existing.Generation, &existing.Fingerprint, &existing.Status, &existing.State, &existing.CreatedAt)
	if err == nil {
		if existing.Fingerprint != fingerprint {
			return model.ReceiverDispatch{}, ErrReceiverGeneration
		}
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return model.ReceiverDispatch{}, err
	}
	receiver, err := receiverTx(ctx, tx, input.ReceiverUID)
	if err != nil {
		return model.ReceiverDispatch{}, err
	}
	if receiver.Version != input.ReceiverVersion {
		return model.ReceiverDispatch{}, ErrReceiverConflict
	}
	var last int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(max(generation),0) FROM alert_receiver_dispatches WHERE receiver_uid=? AND group_key=?`, input.ReceiverUID, input.GroupKey).Scan(&last); err != nil {
		return model.ReceiverDispatch{}, err
	}
	if input.Generation != last+1 {
		return model.ReceiverDispatch{}, ErrReceiverGeneration
	}
	integrationMap := map[string]model.ReceiverIntegration{}
	for _, i := range receiver.Integrations {
		integrationMap[i.UID] = i
	}
	for _, d := range input.Deliveries {
		i, ok := integrationMap[d.IntegrationUID]
		if input.Status == "resolved" {
			if !ok || i.Disabled || i.DisableResolveMessage {
				return model.ReceiverDispatch{}, ErrReceiverSuppressed
			}
			var owner string
			if err = tx.QueryRowContext(ctx, `SELECT receiver_uid FROM alert_receiver_integrations WHERE uid=?`, d.IntegrationUID).Scan(&owner); err != nil {
				return model.ReceiverDispatch{}, err
			}
			if owner != input.ReceiverUID {
				return model.ReceiverDispatch{}, model.ErrInvalidReceiver
			}
			config, e := s.integrationSnapshotTx(ctx, tx, d.IntegrationUID, d.Revision)
			if e != nil {
				return model.ReceiverDispatch{}, e
			}
			i = config.Integration
			ok = true
			integrationMap[d.IntegrationUID] = i
		}
		if !ok || i.Revision != d.Revision || i.Disabled || i.Type == "webhook" || input.Status == "resolved" && i.DisableResolveMessage {
			return model.ReceiverDispatch{}, model.ErrInvalidReceiver
		}
		expected := "slack"
		if i.Type == "email" {
			expected = "smtp"
		}
		if d.Prepared.Protocol() != expected {
			return model.ReceiverDispatch{}, model.ErrInvalidReceiver
		}
		if input.Status == "resolved" {
			for _, op := range d.Prepared.Operations() {
				for _, p := range d.Periods[op.Key] {
					var count int
					if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM alert_receiver_firing_receipts WHERE integration_uid=? AND channel_epoch=? AND period_key=? AND target_key=?`, i.UID, i.ChannelEpoch, s.receiverPeriodKey(p), s.receiverTargetKey(expected, op.Target)).Scan(&count); err != nil {
						return model.ReceiverDispatch{}, err
					}
					if count != 1 {
						return model.ReceiverDispatch{}, ErrReceiverUnacknowledged
					}
				}
			}
		}
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO alert_receiver_dispatches(receiver_uid,group_key,generation,fingerprint,status,created_at,sealed) VALUES(?,?,?,?,?,?,X'')`, input.ReceiverUID, input.GroupKey, input.Generation, fingerprint, input.Status, input.CreatedAt)
	if err != nil {
		return model.ReceiverDispatch{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return model.ReceiverDispatch{}, err
	}
	sealed, err := s.sealReceiver("dispatch", fmt.Sprint(id), input.Generation, freeze)
	if err != nil {
		return model.ReceiverDispatch{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE alert_receiver_dispatches SET sealed=? WHERE id=?`, sealed, id); err != nil {
		return model.ReceiverDispatch{}, err
	}
	for _, d := range input.Deliveries {
		i := integrationMap[d.IntegrationUID]
		snapshot := receiverPreparedSnapshot{d.Prepared, d.Periods}
		deliveryFingerprint := s.receiverHash("delivery-fingerprint", []any{fingerprint, i.UID, i.Revision, i.ChannelEpoch, snapshot})
		result, err = tx.ExecContext(ctx, `INSERT INTO alert_receiver_deliveries(dispatch_id,integration_uid,revision,channel_epoch,fingerprint,next_attempt_at,created_at,sealed) VALUES(?,?,?,?,?,?,?,X'')`, id, i.UID, i.Revision, i.ChannelEpoch, deliveryFingerprint, input.CreatedAt, input.CreatedAt)
		if err != nil {
			return model.ReceiverDispatch{}, err
		}
		deliveryID, err := result.LastInsertId()
		if err != nil {
			return model.ReceiverDispatch{}, err
		}
		sealed, err = s.sealReceiver("delivery", fmt.Sprint(deliveryID), i.Revision, snapshot)
		if err != nil {
			return model.ReceiverDispatch{}, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE alert_receiver_deliveries SET sealed=? WHERE id=?`, sealed, deliveryID); err != nil {
			return model.ReceiverDispatch{}, err
		}
		for _, op := range d.Prepared.Operations() {
			if _, err = tx.ExecContext(ctx, `INSERT INTO alert_receiver_operations(delivery_id,op_key,target_key,next_attempt_at) VALUES(?,?,?,?)`, deliveryID, op.Key, s.receiverTargetKey(d.Prepared.Protocol(), op.Target), input.CreatedAt); err != nil {
				return model.ReceiverDispatch{}, err
			}
		}
	}
	value := model.ReceiverDispatch{ID: id, ReceiverUID: input.ReceiverUID, GroupKey: input.GroupKey, Generation: input.Generation, Fingerprint: fingerprint, Status: input.Status, State: "pending", CreatedAt: input.CreatedAt}
	return value, tx.Commit()
}

const receiverDeliverySelect = `SELECT d.id,d.dispatch_id,x.receiver_uid,d.integration_uid,d.revision,d.channel_epoch,x.group_key,x.generation,d.fingerprint,x.status,d.state,d.attempts,d.next_attempt_at,d.lease_until,d.created_at,d.completed_at FROM alert_receiver_deliveries d JOIN alert_receiver_dispatches x ON x.id=d.dispatch_id `

func scanReceiverDelivery(row interface{ Scan(...any) error }) (model.ReceiverDelivery, error) {
	var v model.ReceiverDelivery
	err := row.Scan(&v.ID, &v.DispatchID, &v.ReceiverUID, &v.IntegrationUID, &v.Revision, &v.ChannelEpoch, &v.GroupKey, &v.Generation, &v.Fingerprint, &v.Status, &v.State, &v.Attempts, &v.NextAttemptAt, &v.LeaseUntil, &v.CreatedAt, &v.CompletedAt)
	return v, err
}
func (s *Store) ReceiverDeliveries(ctx context.Context, limit int) ([]model.ReceiverDelivery, error) {
	if limit < 1 || limit > 1000 {
		limit = 100
	}
	rows, err := s.DB.QueryContext(ctx, receiverDeliverySelect+`ORDER BY d.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ReceiverDelivery{}
	for rows.Next() {
		v, err := scanReceiverDelivery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) ReceiverOperations(ctx context.Context, deliveryID int64) ([]model.ReceiverOperation, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT delivery_id,op_key,state,attempts,max_attempts,next_attempt_at,code,status_code,receipt FROM alert_receiver_operations WHERE delivery_id=? ORDER BY op_key`, deliveryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ReceiverOperation{}
	for rows.Next() {
		var v model.ReceiverOperation
		if err = rows.Scan(&v.DeliveryID, &v.Key, &v.State, &v.Attempts, &v.MaxAttempts, &v.NextAttemptAt, &v.Code, &v.StatusCode, &v.Receipt.ID); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ClaimReceiverDelivery reclaims expired leases with a new token. It never
// reclaims persisted sent operations, including ACKs committed before a crash.
func (s *Store) ClaimReceiverDelivery(ctx context.Context, at time.Time, lease time.Duration) (*ClaimedReceiverDelivery, error) {
	if at.UnixMilli() < 1 || lease < receivers.MaxTimeout+5*time.Second || lease > 5*time.Minute {
		return nil, model.ErrInvalidReceiver
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	v, err := scanReceiverDelivery(tx.QueryRowContext(ctx, receiverDeliverySelect+`WHERE ((d.state IN ('pending','partial') AND d.next_attempt_at<=?) OR (d.state='sending' AND d.lease_until<=?)) AND NOT EXISTS(SELECT 1 FROM alert_receiver_deliveries p JOIN alert_receiver_dispatches y ON y.id=p.dispatch_id WHERE p.integration_uid=d.integration_uid AND y.group_key=x.group_key AND p.id<d.id AND p.state IN ('pending','partial','sending')) ORDER BY d.id LIMIT 1`, at.UnixMilli(), at.UnixMilli()))
	if err != nil {
		return nil, err
	}
	var sealed []byte
	if err = tx.QueryRowContext(ctx, `SELECT sealed FROM alert_receiver_deliveries WHERE id=?`, v.ID).Scan(&sealed); err != nil {
		return nil, err
	}
	var snapshot receiverPreparedSnapshot
	if err = s.openReceiver("delivery", fmt.Sprint(v.ID), v.Revision, sealed, &snapshot); err != nil {
		return nil, err
	}
	config, err := s.integrationSnapshotTx(ctx, tx, v.IntegrationUID, v.Revision)
	if err != nil {
		return nil, err
	}
	var transport receivers.JournaledTransport
	switch config.Integration.Type {
	case "email":
		profile, e := s.profileSnapshotTx(ctx, tx, config.Integration.SMTPProfileUID, config.Integration.SMTPProfileRevision)
		if e != nil {
			return nil, e
		}
		transport, err = smtpTransport(profile)
	case "slack":
		transport, err = slackTransport(config)
	default:
		return nil, model.ErrInvalidReceiver
	}
	if err != nil {
		return nil, err
	}
	c := &ClaimedReceiverDelivery{ReceiverDelivery: v, token: uuid.NewString(), periods: snapshot.Periods, claimedKeys: map[string]bool{}, progress: receivers.Progress{}, transport: transport}
	committed := false
	defer func() {
		if !committed {
			c.Close()
		}
	}()
	if _, err = tx.ExecContext(ctx, `UPDATE alert_receiver_operations SET state='failed',code='attempts_exhausted' WHERE delivery_id=? AND state='sending' AND attempts>=max_attempts`, v.ID); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT op_key,state,attempts,max_attempts,next_attempt_at,receipt FROM alert_receiver_operations WHERE delivery_id=?`, v.ID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var key, state, receipt string
		var attempts, maxAttempts, next int64
		if err = rows.Scan(&key, &state, &attempts, &maxAttempts, &next, &receipt); err != nil {
			break
		}
		if state == "sent" {
			c.progress[key] = receivers.Receipt{ID: receipt}
		} else if (state == "pending" && next <= at.UnixMilli() || state == "sending" && v.LeaseUntil <= at.UnixMilli()) && attempts < maxAttempts {
			c.claimedKeys[key] = true
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(c.claimedKeys) == 0 {
		// A worker may have checkpointed its last operation and crashed before
		// Finish. Recover the aggregate without ever transmitting those ACKs again.
		if _, err = tx.ExecContext(ctx, `UPDATE alert_receiver_operations SET state='failed',code='attempts_exhausted' WHERE delivery_id=? AND state='sending' AND attempts>=max_attempts`, v.ID); err != nil {
			return nil, err
		}
		if err = s.finishReceiverAggregateTx(ctx, tx, v.ID, at.UnixMilli()); err != nil {
			return nil, err
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		return nil, sql.ErrNoRows
	}
	ops := []receivers.Operation{}
	for _, op := range snapshot.Prepared.Operations() {
		if c.claimedKeys[op.Key] {
			ops = append(ops, op)
		}
	}
	c.prepared, err = receivers.NewPrepared(snapshot.Prepared.Protocol(), ops)
	if err != nil {
		return nil, ErrReceiverSnapshot
	}
	c.LeaseUntil = at.Add(lease).UnixMilli()
	c.State = "sending"
	c.Attempts++
	result, err := tx.ExecContext(ctx, `UPDATE alert_receiver_deliveries SET state='sending',lease_token=?,lease_until=?,attempts=attempts+1 WHERE id=? AND fingerprint=? AND ((state IN ('pending','partial') AND next_attempt_at<=?) OR (state='sending' AND lease_until<=?))`, c.token, c.LeaseUntil, v.ID, v.Fingerprint, at.UnixMilli(), at.UnixMilli())
	if err != nil {
		return nil, err
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		return nil, ErrReceiverLease
	}
	for key := range c.claimedKeys {
		if _, err = tx.ExecContext(ctx, `UPDATE alert_receiver_operations SET state='sending',attempts=attempts+1 WHERE delivery_id=? AND op_key=?`, v.ID, key); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	committed = true
	return c, nil
}

func validateReceiverClaimTx(ctx context.Context, tx *sql.Tx, c *ClaimedReceiverDelivery, at int64) error {
	if c == nil || c.token == "" || at < 1 {
		return ErrReceiverLease
	}
	var count int
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM alert_receiver_deliveries d JOIN alert_receiver_dispatches x ON x.id=d.dispatch_id WHERE d.id=? AND d.state='sending' AND d.lease_token=? AND d.lease_until>? AND d.lease_until=? AND d.fingerprint=? AND x.generation=? AND x.group_key=? AND x.receiver_uid=? AND d.integration_uid=? AND d.revision=? AND d.channel_epoch=? AND d.dispatch_id=? AND x.status=?`, c.ID, c.token, at, c.LeaseUntil, c.Fingerprint, c.Generation, c.GroupKey, c.ReceiverUID, c.IntegrationUID, c.Revision, c.ChannelEpoch, c.DispatchID, c.Status).Scan(&count)
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrReceiverLease
	}
	var raw string
	if err = tx.QueryRowContext(ctx, `SELECT r.config FROM alert_receiver_integrations i JOIN alert_receivers x ON x.uid=i.receiver_uid JOIN alert_receiver_integration_revisions r ON r.uid=i.uid AND r.revision=i.revision WHERE i.uid=? AND i.deleted=0 AND x.deleted=0`, c.IntegrationUID).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrReceiverSuppressed
		}
		return err
	}
	var current model.ReceiverIntegration
	if json.Unmarshal([]byte(raw), &current) != nil {
		return ErrReceiverSnapshot
	}
	if current.Disabled || c.Status == "resolved" && current.DisableResolveMessage {
		return ErrReceiverSuppressed
	}
	return nil
}
func (s *Store) ValidateReceiverClaim(ctx context.Context, c *ClaimedReceiverDelivery, at time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	return validateReceiverClaimTx(ctx, tx, c, at.UnixMilli())
}

var receiverOutcomeCode = regexp.MustCompile(`^[a-z_]{1,64}$`)
var receiverReceiptID = regexp.MustCompile(`^[a-zA-Z0-9.@:_<>-]{1,256}$`)

func validReceiverOutcome(out receivers.Outcome) bool {
	return receiverOutcomeCode.MatchString(out.Code) && out.StatusCode >= 0 && out.StatusCode <= 599 && out.RetryAfter >= 0 && (!out.Sent || receiverReceiptID.MatchString(out.Receipt.ID))
}

func (s *Store) checkpointReceiverTx(ctx context.Context, tx *sql.Tx, c *ClaimedReceiverDelivery, outcomes []receivers.Outcome, at int64) error {
	if err := validateReceiverClaimTx(ctx, tx, c, at); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, out := range outcomes {
		if !out.Sent || out.Skipped || !c.claimedKeys[out.OperationKey] || seen[out.OperationKey] || !validReceiverOutcome(out) {
			return model.ErrInvalidReceiver
		}
		seen[out.OperationKey] = true
		var state, receipt, target string
		if err := tx.QueryRowContext(ctx, `SELECT state,receipt,target_key FROM alert_receiver_operations WHERE delivery_id=? AND op_key=?`, c.ID, out.OperationKey).Scan(&state, &receipt, &target); err != nil {
			return err
		}
		if state == "sent" {
			if receipt != out.Receipt.ID {
				return ErrReceiverLease
			}
			continue
		}
		if state != "sending" {
			return ErrReceiverLease
		}
		if _, err := tx.ExecContext(ctx, `UPDATE alert_receiver_operations SET state='sent',code='sent',status_code=?,receipt=? WHERE delivery_id=? AND op_key=? AND state='sending'`, out.StatusCode, out.Receipt.ID, c.ID, out.OperationKey); err != nil {
			return err
		}
		if c.Status == "firing" {
			for _, p := range c.periods[out.OperationKey] {
				if _, err := tx.ExecContext(ctx, `INSERT INTO alert_receiver_firing_receipts VALUES(?,?,?,?,?,?,?) ON CONFLICT(integration_uid,channel_epoch,period_key,target_key) DO NOTHING`, c.IntegrationUID, c.ChannelEpoch, s.receiverPeriodKey(p), target, c.ID, out.OperationKey, at); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
func (s *Store) CheckpointReceiverDelivery(ctx context.Context, c *ClaimedReceiverDelivery, outcomes []receivers.Outcome, at time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s.checkpointReceiverTx(ctx, tx, c, outcomes, at.UnixMilli()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) finishReceiverAggregateTx(ctx context.Context, tx *sql.Tx, id, at int64) error {
	var sent, pending, sending, failed, canceled int
	var next int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(sum(state='sent'),0),COALESCE(sum(state='pending'),0),COALESCE(sum(state='sending'),0),COALESCE(sum(state='failed'),0),COALESCE(sum(state='canceled'),0),COALESCE(min(CASE WHEN state='pending' THEN next_attempt_at END),0) FROM alert_receiver_operations WHERE delivery_id=?`, id).Scan(&sent, &pending, &sending, &failed, &canceled, &next); err != nil {
		return err
	}
	state := "sent"
	completed := at
	if sending > 0 {
		return ErrReceiverLease
	}
	if pending > 0 {
		state = "pending"
		if sent > 0 {
			state = "partial"
		}
		completed = 0
	} else if failed > 0 {
		state = "failed"
	} else if canceled > 0 || sent == 0 {
		state = "canceled"
	}
	if _, err := tx.ExecContext(ctx, `UPDATE alert_receiver_deliveries SET state=?,next_attempt_at=?,lease_token='',lease_until=0,completed_at=? WHERE id=?`, state, next, completed, id); err != nil {
		return err
	}
	var dispatchID int64
	if err := tx.QueryRowContext(ctx, `SELECT dispatch_id FROM alert_receiver_deliveries WHERE id=?`, id).Scan(&dispatchID); err != nil {
		return err
	}
	var unfinished, failures, canceledDeliveries int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(sum(state IN ('pending','partial','sending')),0),COALESCE(sum(state='failed'),0),COALESCE(sum(state='canceled'),0) FROM alert_receiver_deliveries WHERE dispatch_id=?`, dispatchID).Scan(&unfinished, &failures, &canceledDeliveries); err != nil {
		return err
	}
	dispatchState := "sent"
	if unfinished > 0 {
		dispatchState = "pending"
	} else if failures > 0 {
		dispatchState = "failed"
	} else if canceledDeliveries > 0 {
		dispatchState = "canceled"
	}
	_, err := tx.ExecContext(ctx, `UPDATE alert_receiver_dispatches SET state=? WHERE id=?`, dispatchState, dispatchID)
	return err
}

// Explicit user changes cancel unsent work atomically with the configuration
// CAS. ACKs survive; queued resolved messages never bypass a current disable.
// Re-enabling requires a fresh scheduler generation rather than replaying stale
// messages. Silence/inhibition/group suppression is owned by the future worker.
func (s *Store) cancelReceiverIntegrationTx(ctx context.Context, tx *sql.Tx, uid string, resolvedOnly bool, code string, at int64) error {
	query := `SELECT d.id FROM alert_receiver_deliveries d JOIN alert_receiver_dispatches x ON x.id=d.dispatch_id WHERE d.integration_uid=? AND d.state IN ('pending','partial','sending')`
	if resolvedOnly {
		query += ` AND x.status='resolved'`
	}
	rows, err := tx.QueryContext(ctx, query, uid)
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
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err = tx.ExecContext(ctx, `UPDATE alert_receiver_operations SET state='canceled',code=? WHERE delivery_id=? AND state IN ('pending','sending')`, code, id); err != nil {
			return err
		}
		if err = s.finishReceiverAggregateTx(ctx, tx, id, at); err != nil {
			return err
		}
	}
	return nil
}

// FinishReceiverDelivery persists remaining outcomes atomically. A successful
// remote ACK is never converted into a failure because its checkpoint failed.
// If this commit also fails, the unavoidable remote-ACK/DB crash window remains
// at least once; the caller must stop and not start another transport operation.
func (s *Store) FinishReceiverDelivery(ctx context.Context, c *ClaimedReceiverDelivery, outcomes []receivers.Outcome, at time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := at.UnixMilli()
	if err = validateReceiverClaimTx(ctx, tx, c, now); err != nil {
		return err
	}
	indexed := map[string]receivers.Outcome{}
	success := []receivers.Outcome{}
	for _, out := range outcomes {
		if !c.claimedKeys[out.OperationKey] || !validReceiverOutcome(out) {
			return model.ErrInvalidReceiver
		}
		if _, ok := indexed[out.OperationKey]; ok {
			return model.ErrInvalidReceiver
		}
		indexed[out.OperationKey] = out
		if out.Sent && !out.Skipped {
			success = append(success, out)
		}
	}
	if len(success) > 0 {
		if err = s.checkpointReceiverTx(ctx, tx, c, success, now); err != nil {
			return err
		}
	}
	for key := range c.claimedKeys {
		var state string
		var attempts, max int64
		if err = tx.QueryRowContext(ctx, `SELECT state,attempts,max_attempts FROM alert_receiver_operations WHERE delivery_id=? AND op_key=?`, c.ID, key).Scan(&state, &attempts, &max); err != nil {
			return err
		}
		if state == "sent" {
			continue
		}
		if state != "sending" {
			return ErrReceiverLease
		}
		out, ok := indexed[key]
		if !ok {
			out = receivers.Outcome{OperationKey: key, Retryable: true, Code: "missing_outcome"}
		}
		if out.Sent {
			return ErrReceiverLease
		}
		next := int64(0)
		state = "failed"
		code := out.Code
		if out.Retryable && attempts < max {
			state = "pending"
			delay := time.Second * time.Duration(1<<min(attempts-1, 8))
			if delay < out.RetryAfter {
				delay = out.RetryAfter
			}
			next = at.Add(delay).UnixMilli()
		} else if out.Retryable {
			code = "attempts_exhausted"
		}
		if _, err = tx.ExecContext(ctx, `UPDATE alert_receiver_operations SET state=?,next_attempt_at=?,code=?,status_code=? WHERE delivery_id=? AND op_key=? AND state='sending'`, state, next, code, out.StatusCode, c.ID, key); err != nil {
			return err
		}
	}
	if err = s.finishReceiverAggregateTx(ctx, tx, c.ID, now); err != nil {
		return err
	}
	return tx.Commit()
}

// SendClaimedReceiverDelivery is the safe execution entry point for a future
// worker. It performs the current configuration fence immediately before the
// transport, journals ACKs synchronously, and finishes with a bounded context
// even when the caller canceled the network request. No background work starts.
func (s *Store) SendClaimedReceiverDelivery(ctx context.Context, c *ClaimedReceiverDelivery) ([]receivers.Outcome, error) {
	if c == nil {
		return nil, ErrReceiverLease
	}
	now := time.Now()
	deadline := time.UnixMilli(c.LeaseUntil).Add(-5 * time.Second)
	if !deadline.After(now) {
		return nil, ErrReceiverLease
	}
	work, sendToken, err := s.beginReceiverSend(ctx, c, now)
	if err != nil {
		return nil, err
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, _ = s.DB.ExecContext(releaseCtx, `UPDATE alert_receiver_send_leases SET send_token='' WHERE delivery_id=? AND claim_token=? AND send_token=?`, c.ID, c.token, sendToken)
	}()
	networkCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	var out []receivers.Outcome
	if len(work.claimedKeys) > 0 {
		out = work.Transport().SendJournaled(networkCtx, work.Prepared(), work.Progress(), func(ctx context.Context, out []receivers.Outcome) error {
			return s.CheckpointReceiverDelivery(ctx, work, out, time.Now())
		})
	}
	finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer finishCancel()
	if err := s.FinishReceiverDelivery(finishCtx, work, out, time.Now()); err != nil {
		return out, err
	}
	return out, nil
}

// The DB latch serializes even copied handles or different Store connections.
// Durable progress is refreshed while acquiring it: an ACK checkpoint can commit
// before aggregate Finish fails, and must never be replayed on a same-lease retry.
// A newly reclaimed claim may replace an orphan latch from an expired process.
func (s *Store) beginReceiverSend(ctx context.Context, c *ClaimedReceiverDelivery, at time.Time) (*ClaimedReceiverDelivery, string, error) {
	// WAL snapshots cannot always be promoted to a write transaction. Retry only
	// SQLite's typed contention codes, and always with a NEW rolled-back snapshot.
	// Existing callers' cancellation/deadlines take precedence over this bound.
	retryCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	started := time.Now()
	delay := 5 * time.Millisecond
	for {
		if err := retryCtx.Err(); err != nil {
			return nil, "", err
		}
		work, tag, err := s.beginReceiverSendOnce(retryCtx, c, at.Add(time.Since(started)))
		if err == nil {
			return work, tag, nil
		}
		if contextErr := retryCtx.Err(); contextErr != nil {
			return nil, "", contextErr
		}
		if !receiverSQLiteContention(err) {
			return nil, "", err
		}
		timer := time.NewTimer(delay)
		select {
		case <-retryCtx.Done():
			timer.Stop()
			return nil, "", retryCtx.Err()
		case <-timer.C:
		}
		delay = min(2*delay, 50*time.Millisecond)
	}
}

func receiverSQLiteContention(err error) bool {
	var code interface{ Code() int }
	if !errors.As(err, &code) {
		return false
	}
	// Extended codes (for example SQLITE_BUSY_SNAPSHOT) retain the primary
	// SQLITE_BUSY=5 or SQLITE_LOCKED=6 in their low byte (pinned modernc SQLite).
	return code.Code()&255 == 5 || code.Code()&255 == 6
}

func (s *Store) beginReceiverSendOnce(ctx context.Context, c *ClaimedReceiverDelivery, at time.Time) (*ClaimedReceiverDelivery, string, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback()
	if err = validateReceiverClaimTx(ctx, tx, c, at.UnixMilli()); err != nil {
		return nil, "", err
	}
	var currentClaim, currentSend string
	err = tx.QueryRowContext(ctx, `SELECT claim_token,send_token FROM alert_receiver_send_leases WHERE delivery_id=?`, c.ID).Scan(&currentClaim, &currentSend)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, "", err
	}
	if err == nil && currentClaim == c.token && currentSend != "" {
		return nil, "", ErrReceiverSending
	}
	tag := uuid.NewString()
	result, err := tx.ExecContext(ctx, `INSERT INTO alert_receiver_send_leases(delivery_id,claim_token,send_token,execution_count) VALUES(?,?,?,1) ON CONFLICT(delivery_id) DO UPDATE SET claim_token=excluded.claim_token,send_token=excluded.send_token,execution_count=CASE WHEN alert_receiver_send_leases.claim_token=excluded.claim_token THEN alert_receiver_send_leases.execution_count+1 ELSE 1 END WHERE alert_receiver_send_leases.send_token='' OR alert_receiver_send_leases.claim_token<>excluded.claim_token`, c.ID, c.token, tag)
	if err != nil {
		return nil, "", err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, "", err
	}
	if affected != 1 {
		return nil, "", ErrReceiverSending
	}
	var executions int64
	if err = tx.QueryRowContext(ctx, `SELECT execution_count FROM alert_receiver_send_leases WHERE delivery_id=?`, c.ID).Scan(&executions); err != nil {
		return nil, "", err
	}
	if executions > 1 {
		// The first execution consumes the attempts reserved by Claim. A retry
		// under that SAME lease must reserve another attempt for unsent work.
		if _, err = tx.ExecContext(ctx, `UPDATE alert_receiver_operations SET state='failed',code='attempts_exhausted' WHERE delivery_id=? AND state='sending' AND attempts>=max_attempts`, c.ID); err != nil {
			return nil, "", err
		}
		result, err = tx.ExecContext(ctx, `UPDATE alert_receiver_operations SET attempts=attempts+1 WHERE delivery_id=? AND state='sending'`, c.ID)
		if err != nil {
			return nil, "", err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return nil, "", err
		}
		if count > 0 {
			if _, err = tx.ExecContext(ctx, `UPDATE alert_receiver_deliveries SET attempts=attempts+1 WHERE id=?`, c.ID); err != nil {
				return nil, "", err
			}
		}
	}
	var sealed []byte
	if err = tx.QueryRowContext(ctx, `SELECT sealed FROM alert_receiver_deliveries WHERE id=?`, c.ID).Scan(&sealed); err != nil {
		return nil, "", err
	}
	var snapshot receiverPreparedSnapshot
	if err = s.openReceiver("delivery", fmt.Sprint(c.ID), c.Revision, sealed, &snapshot); err != nil {
		return nil, "", err
	}
	work := &ClaimedReceiverDelivery{ReceiverDelivery: c.ReceiverDelivery, token: c.token, transport: c.transport, periods: snapshot.Periods, claimedKeys: map[string]bool{}, progress: receivers.Progress{}}
	states := map[string]string{}
	rows, err := tx.QueryContext(ctx, `SELECT op_key,state,receipt FROM alert_receiver_operations WHERE delivery_id=?`, c.ID)
	if err != nil {
		return nil, "", err
	}
	for rows.Next() {
		var key, state, receipt string
		if err = rows.Scan(&key, &state, &receipt); err != nil {
			break
		}
		states[key] = state
		if state == "sent" {
			work.progress[key] = receivers.Receipt{ID: receipt}
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, "", err
	}
	operations := []receivers.Operation{}
	found := 0
	for _, op := range snapshot.Prepared.Operations() {
		if c.claimedKeys[op.Key] {
			found++
			if states[op.Key] == "failed" {
				continue
			}
			if states[op.Key] != "sending" && states[op.Key] != "sent" {
				return nil, "", ErrReceiverLease
			}
			work.claimedKeys[op.Key] = true
			operations = append(operations, op)
		}
	}
	if found != len(c.claimedKeys) {
		return nil, "", ErrReceiverSnapshot
	}
	if len(operations) > 0 {
		work.prepared, err = receivers.NewPrepared(snapshot.Prepared.Protocol(), operations)
		if err != nil {
			return nil, "", ErrReceiverSnapshot
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, "", err
	}
	return work, tag, nil
}

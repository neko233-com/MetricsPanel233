package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/neko233-com/MetricsPanel233/internal/model"
)

type compiledInhibitionMatcher struct {
	name, operator, value string
	re                    *regexp.Regexp
}
type compiledInhibition struct {
	rule           model.InhibitionRule
	source, target []compiledInhibitionMatcher
	equal          []string
}

// Entries are immutable, shared safely by dispatch/evaluation, and bounded even
// when users repeatedly preview new drafts. No matcher is compiled per alert.
var inhibitionMatcherCache = struct {
	sync.Mutex
	values map[string]*compiledInhibition
	order  []string
}{values: map[string]*compiledInhibition{}}

func compileInhibition(rule model.InhibitionRule) (*compiledInhibition, error) {
	raw, _ := json.Marshal(rule)
	key := string(raw)
	inhibitionMatcherCache.Lock()
	defer inhibitionMatcherCache.Unlock()
	if c := inhibitionMatcherCache.values[key]; c != nil {
		return c, nil
	}
	c := &compiledInhibition{rule: rule, equal: append([]string{}, rule.Equal...)}
	sort.Strings(c.equal)
	for side, list := range [][]model.NotificationMatcher{rule.SourceMatchers, rule.TargetMatchers} {
		compiled := []compiledInhibitionMatcher{}
		for _, m := range list {
			v := compiledInhibitionMatcher{name: m.Name, operator: m.Operator, value: m.Value}
			if m.Operator == "=~" || m.Operator == "!~" {
				var err error
				v.re, err = regexp.Compile("^(?:" + m.Value + ")$")
				if err != nil {
					return nil, err
				}
			}
			compiled = append(compiled, v)
		}
		if side == 0 {
			c.source = compiled
		} else {
			c.target = compiled
		}
	}
	if len(inhibitionMatcherCache.order) >= 256 {
		delete(inhibitionMatcherCache.values, inhibitionMatcherCache.order[0])
		inhibitionMatcherCache.order = inhibitionMatcherCache.order[1:]
	}
	inhibitionMatcherCache.values[key] = c
	inhibitionMatcherCache.order = append(inhibitionMatcherCache.order, key)
	return c, nil
}
func inhibitionMatches(matchers []compiledInhibitionMatcher, labels map[string]string) bool {
	for _, m := range matchers {
		value := labels[m.name]
		switch m.operator {
		case "=":
			if value != m.value {
				return false
			}
		case "!=":
			if value == m.value {
				return false
			}
		case "=~":
			if !m.re.MatchString(value) {
				return false
			}
		case "!~":
			if m.re.MatchString(value) {
				return false
			}
		default:
			return false
		}
	}
	return true
}
func (c *compiledInhibition) equalKey(labels map[string]string) string {
	values := make([]string, len(c.equal))
	for i, name := range c.equal {
		values[i] = labels[name]
	}
	raw, _ := json.Marshal(values)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (s *Store) initInhibition() error {
	_, err := s.DB.Exec(`CREATE TABLE IF NOT EXISTS alert_inhibition_rules(uid TEXT PRIMARY KEY,version INTEGER NOT NULL,config TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS alert_inhibition_active(rule_uid TEXT NOT NULL,instance_key TEXT NOT NULL,labels TEXT NOT NULL,state TEXT NOT NULL,started_at INTEGER NOT NULL,PRIMARY KEY(rule_uid,instance_key));
CREATE TABLE IF NOT EXISTS alert_inhibition_sources(inhibition_uid TEXT NOT NULL REFERENCES alert_inhibition_rules(uid) ON DELETE CASCADE,source_rule TEXT NOT NULL,source_key TEXT NOT NULL,equal_key TEXT NOT NULL,two_sided INTEGER NOT NULL,PRIMARY KEY(inhibition_uid,source_rule,source_key));
CREATE INDEX IF NOT EXISTS alert_inhibition_equal ON alert_inhibition_sources(inhibition_uid,equal_key,two_sided);
CREATE INDEX IF NOT EXISTS alert_inhibition_source_instance ON alert_inhibition_sources(source_rule,source_key);`)
	if err != nil {
		return err
	}
	ctx := context.Background()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Runtime is authoritative after migration/restart; delivery/history pruning
	// cannot remove an inhibiting source. Rebuild before any worker is started.
	rows, err := tx.QueryContext(ctx, `SELECT config,runtime FROM alert_rules`)
	if err != nil {
		return err
	}
	views := []model.AlertRuleView{}
	for rows.Next() {
		v, e := scanRule(rows)
		if e != nil {
			rows.Close()
			return e
		}
		views = append(views, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM alert_inhibition_sources`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM alert_inhibition_active`); err != nil {
		return err
	}
	for _, v := range views {
		if err := syncInhibitionRuntimeTx(ctx, tx, v.AlertRule, v.Runtime, time.Now().UnixMilli(), false); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func scanInhibition(row interface{ Scan(...any) error }) (model.InhibitionRule, error) {
	var v model.InhibitionRule
	var raw string
	err := row.Scan(&raw)
	if err == nil {
		err = json.Unmarshal([]byte(raw), &v)
	}
	return v, err
}
func inhibitionRulesTx(ctx context.Context, tx *sql.Tx) ([]*compiledInhibition, error) {
	rows, err := tx.QueryContext(ctx, `SELECT config FROM alert_inhibition_rules ORDER BY uid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*compiledInhibition{}
	for rows.Next() {
		v, err := scanInhibition(rows)
		if err != nil {
			return nil, err
		}
		if !v.Disabled {
			c, err := compileInhibition(v)
			if err != nil {
				return nil, err
			}
			out = append(out, c)
		}
	}
	return out, rows.Err()
}
func (s *Store) InhibitionRules(ctx context.Context) ([]model.InhibitionRule, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT config FROM alert_inhibition_rules ORDER BY json_extract(config,'$.name') COLLATE NOCASE,uid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.InhibitionRule{}
	for rows.Next() {
		v, e := scanInhibition(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) InhibitionRule(ctx context.Context, uid string) (model.InhibitionRule, error) {
	return scanInhibition(s.DB.QueryRowContext(ctx, `SELECT config FROM alert_inhibition_rules WHERE uid=?`, uid))
}
func (s *Store) SaveInhibitionRule(ctx context.Context, v model.InhibitionRule) (model.InhibitionRule, error) {
	if v.UID == "" {
		v.UID = uuid.NewString()
	}
	if v.Equal == nil {
		v.Equal = []string{}
	}
	if err := v.Validate(); err != nil {
		return v, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return v, err
	}
	defer tx.Rollback()
	var version int
	err = tx.QueryRowContext(ctx, `SELECT version FROM alert_inhibition_rules WHERE uid=?`, v.UID).Scan(&version)
	creating := errors.Is(err, sql.ErrNoRows)
	if err != nil && !creating {
		return v, err
	}
	if (creating && v.Version != 0) || (!creating && (v.Version == 0 || v.Version != version)) {
		return v, ErrNotificationConflict
	}
	if creating {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM alert_inhibition_rules`).Scan(&count); err != nil {
			return v, err
		}
		if count >= 100 {
			return v, errors.New("at most 100 inhibition rules")
		}
	}
	v.Version = version + 1
	v.UpdatedAt = time.Now().UnixMilli()
	raw, _ := json.Marshal(v)
	if _, err = tx.ExecContext(ctx, `INSERT INTO alert_inhibition_rules(uid,version,config) VALUES(?,?,?) ON CONFLICT(uid) DO UPDATE SET version=excluded.version,config=excluded.config`, v.UID, v.Version, string(raw)); err != nil {
		return v, err
	}
	if err := reindexInhibitionTx(ctx, tx, v); err != nil {
		return v, err
	}
	if err := wakeInhibitionTx(ctx, tx, v.UpdatedAt); err != nil {
		return v, err
	}
	return v, tx.Commit()
}
func (s *Store) DeleteInhibitionRule(ctx context.Context, uid string, version int) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var actual int
	if err = tx.QueryRowContext(ctx, `SELECT version FROM alert_inhibition_rules WHERE uid=?`, uid).Scan(&actual); err != nil {
		return err
	}
	if version == 0 || actual != version {
		return ErrNotificationConflict
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM alert_inhibition_rules WHERE uid=? AND version=?`, uid, version); err != nil {
		return err
	}
	if err := wakeInhibitionTx(ctx, tx, time.Now().UnixMilli()); err != nil {
		return err
	}
	return tx.Commit()
}
func reindexInhibitionTx(ctx context.Context, tx *sql.Tx, rule model.InhibitionRule) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM alert_inhibition_sources WHERE inhibition_uid=?`, rule.UID); err != nil {
		return err
	}
	if rule.Disabled {
		return nil
	}
	c, err := compileInhibition(rule)
	if err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT rule_uid,instance_key,labels,state,started_at FROM alert_inhibition_active`)
	if err != nil {
		return err
	}
	items := []model.InhibitionActiveInstance{}
	for rows.Next() {
		v, err := scanInhibitionActive(rows)
		if err != nil {
			rows.Close()
			return err
		}
		if inhibitionMatches(c.source, v.Labels) {
			items = append(items, v)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, v := range items {
		if err := indexInhibitionSourceTx(ctx, tx, c, v); err != nil {
			return err
		}
	}
	return nil
}
func indexInhibitionSourceTx(ctx context.Context, tx *sql.Tx, c *compiledInhibition, v model.InhibitionActiveInstance) error {
	if !inhibitionMatches(c.source, v.Labels) {
		return nil
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO alert_inhibition_sources(inhibition_uid,source_rule,source_key,equal_key,two_sided) VALUES(?,?,?,?,?)`, c.rule.UID, v.RuleUID, v.InstanceKey, c.equalKey(v.Labels), inhibitionMatches(c.target, v.Labels))
	return err
}

func syncInhibitionRuntimeTx(ctx context.Context, tx *sql.Tx, rule model.AlertRule, runtime model.AlertRuntime, at int64, wake bool) error {
	active := []model.InhibitionActiveInstance{}
	if !rule.Paused && rule.Record == "" {
		for _, i := range runtime.Instances {
			if notificationActive(i.State) {
				started := i.FiringAt
				if started == 0 {
					started = i.ActiveAt
				}
				if started == 0 {
					started = at
				}
				if err := tx.QueryRowContext(ctx, `SELECT started_at FROM alert_notification_active WHERE rule_uid=? AND instance_key=?`, rule.UID, i.Key).Scan(&started); err != nil && !errors.Is(err, sql.ErrNoRows) {
					return err
				}
				active = append(active, model.InhibitionActiveInstance{RuleUID: rule.UID, InstanceKey: i.Key, Labels: i.Labels, State: i.State, StartedAt: started})
			}
		}
	}
	if len(active) > 1000 {
		return errors.New("inhibition runtime supports at most 1000 active instances per alert rule")
	}
	// Compare labels/active identities, not evaluation timestamps or values. An
	// unchanged firing evaluation must not wake every notification group.
	rows, err := tx.QueryContext(ctx, `SELECT instance_key,labels FROM alert_inhibition_active WHERE rule_uid=?`, rule.UID)
	if err != nil {
		return err
	}
	previous := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			rows.Close()
			return err
		}
		previous[k] = v
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	changed := len(previous) != len(active)
	for _, v := range active {
		raw, _ := json.Marshal(v.Labels)
		if previous[v.InstanceKey] != string(raw) {
			changed = true
		}
	}
	cs := []*compiledInhibition{}
	if changed {
		if _, err := tx.ExecContext(ctx, `DELETE FROM alert_inhibition_active WHERE rule_uid=?`, rule.UID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM alert_inhibition_sources WHERE source_rule=?`, rule.UID); err != nil {
			return err
		}
		cs, err = inhibitionRulesTx(ctx, tx)
		if err != nil {
			return err
		}
	}
	for _, v := range active {
		raw, _ := json.Marshal(v.Labels)
		if _, err := tx.ExecContext(ctx, `INSERT INTO alert_inhibition_active(rule_uid,instance_key,labels,state,started_at) VALUES(?,?,?,?,?) ON CONFLICT(rule_uid,instance_key) DO UPDATE SET state=excluded.state,started_at=excluded.started_at`, v.RuleUID, v.InstanceKey, string(raw), v.State, v.StartedAt); err != nil {
			return err
		}
		for _, c := range cs {
			if err := indexInhibitionSourceTx(ctx, tx, c, v); err != nil {
				return err
			}
		}
	}
	if changed && wake && len(cs) > 0 {
		return wakeInhibitionTx(ctx, tx, at)
	}
	return nil
}
func wakeInhibitionTx(ctx context.Context, tx *sql.Tx, at int64) error {
	if _, err := tx.ExecContext(ctx, `UPDATE alert_notification_outbox SET next_attempt_at=? WHERE state='suppressed'`, at); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE alert_notification_groups SET next_at=?,dirty=1,generation=generation+1`, at)
	return err
}

type notificationSuppressor struct {
	ctx      context.Context
	tx       *sql.Tx
	at       int64
	silences []model.Silence
	rules    []*compiledInhibition
}

func newNotificationSuppressor(ctx context.Context, tx *sql.Tx, at int64, silences []model.Silence) (*notificationSuppressor, error) {
	cs, err := inhibitionRulesTx(ctx, tx)
	return &notificationSuppressor{ctx: ctx, tx: tx, at: at, silences: silences, rules: cs}, err
}
func (s *notificationSuppressor) check(labels map[string]string) (int64, string, error) {
	until := silenceUntil(s.silences, labels)
	message := ""
	if until > 0 {
		message = "Silenced"
	}
	for _, c := range s.rules {
		if !inhibitionMatches(c.target, labels) {
			continue
		}
		var found int
		if err := s.tx.QueryRowContext(s.ctx, `SELECT EXISTS(SELECT 1 FROM alert_inhibition_sources WHERE inhibition_uid=? AND equal_key=? AND (?=0 OR two_sided=0))`, c.rule.UID, c.equalKey(labels), inhibitionMatches(c.source, labels)).Scan(&found); err != nil {
			return 0, "", err
		}
		if found > 0 {
			if message != "" {
				message += "; "
			}
			message += "Inhibited"
			until = earlierCheck(until, s.at+5000)
			break
		}
	}
	return until, message, nil
}
func currentNotificationLabelsTx(ctx context.Context, tx *sql.Tx, rule, key, raw string) (map[string]string, error) {
	var labels string
	err := tx.QueryRowContext(ctx, `SELECT labels FROM alert_inhibition_active WHERE rule_uid=? AND instance_key=?`, rule, key).Scan(&labels)
	if errors.Is(err, sql.ErrNoRows) {
		return notificationLabels(raw)
	}
	if err != nil {
		return nil, err
	}
	var out map[string]string
	err = json.Unmarshal([]byte(labels), &out)
	return out, err
}

// Rendering is synchronous but may take time. Recheck the same lease after
// rendering and immediately before the HTTP request, preserving its frozen body
// when it must wait. An already-started network operation is not retractable.
func (s *Store) ValidateNotificationClaim(ctx context.Context, delivery model.NotificationDelivery, at int64) (bool, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var raw string
	var state string
	var attempts int
	var attemptedAt int64
	err = tx.QueryRowContext(ctx, `SELECT payload,state,attempts,last_attempt_at FROM alert_notification_outbox WHERE id=?`, delivery.ID).Scan(&raw, &state, &attempts, &attemptedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if state != "sending" || attempts != delivery.Attempts || attemptedAt != delivery.LastAttemptAt {
		return false, nil
	}
	if delivery.Status == "test" {
		return true, tx.Commit()
	}
	silences, err := activeSilencesTx(ctx, tx, at)
	if err != nil {
		return false, err
	}
	suppressor, err := newNotificationSuppressor(ctx, tx, at, silences)
	if err != nil {
		return false, err
	}
	grouped, valid, err := validGroupNotificationTx(ctx, tx, delivery.ID, at, suppressor)
	if err != nil {
		return false, err
	}
	if grouped {
		return valid, tx.Commit()
	}
	labels, err := currentNotificationLabelsTx(ctx, tx, delivery.RuleUID, delivery.InstanceKey, raw)
	if err != nil {
		return false, err
	}
	until, reason, err := suppressor.check(labels)
	if err != nil {
		return false, err
	}
	if until == 0 {
		return true, tx.Commit()
	}
	state = "suppressed"
	if delivery.Status == "resolved" {
		state = "canceled"
		until = 0
		reason = "Resolved while suppressed"
	}
	_, err = tx.ExecContext(ctx, `UPDATE alert_notification_outbox SET state=?,last_error=?,next_attempt_at=?,attempts=attempts-1 WHERE id=? AND state='sending' AND attempts=? AND last_attempt_at=?`, state, reason, until, delivery.ID, delivery.Attempts, delivery.LastAttemptAt)
	if err != nil {
		return false, err
	}
	return false, tx.Commit()
}
func scanInhibitionActive(row interface{ Scan(...any) error }) (model.InhibitionActiveInstance, error) {
	var v model.InhibitionActiveInstance
	var raw string
	err := row.Scan(&v.RuleUID, &v.InstanceKey, &raw, &v.State, &v.StartedAt)
	if err == nil {
		err = json.Unmarshal([]byte(raw), &v.Labels)
	}
	return v, err
}
func (s *Store) InhibitionActive(ctx context.Context, limit int) (model.InhibitionActiveView, error) {
	out := model.InhibitionActiveView{Instances: []model.InhibitionActiveInstance{}}
	if limit < 1 || limit > 200 {
		return out, errors.New("active instance limit must be 1–200")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM alert_inhibition_active`).Scan(&out.Total); err != nil {
		return out, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT rule_uid,instance_key,labels,state,started_at FROM alert_inhibition_active ORDER BY rule_uid,instance_key LIMIT ?`, limit)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		v, e := scanInhibitionActive(rows)
		if e != nil {
			rows.Close()
			return out, e
		}
		out.Instances = append(out.Instances, v)
	}
	err = rows.Err()
	rows.Close()
	out.Truncated = out.Total > len(out.Instances)
	return out, err
}
func (s *Store) PreviewInhibition(ctx context.Context, input model.InhibitionPreviewInput) (model.InhibitionPreview, error) {
	out := model.InhibitionPreview{Sources: []model.InhibitionActiveInstance{}, Targets: []model.InhibitionPreviewTarget{}}
	if err := model.ValidateInhibitionMatchers(input.SourceMatchers, input.TargetMatchers, input.Equal); err != nil {
		return out, err
	}
	if input.Labels != nil {
		if err := model.ValidateLabels(*input.Labels); err != nil {
			return out, err
		}
	}
	if input.Limit == 0 {
		input.Limit = 50
	}
	if input.Limit < 1 || input.Limit > 200 {
		return out, errors.New("preview limit must be 1–200")
	}
	c, err := compileInhibition(model.InhibitionRule{SourceMatchers: input.SourceMatchers, TargetMatchers: input.TargetMatchers, Equal: input.Equal})
	if err != nil {
		return out, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT rule_uid,instance_key,labels,state,started_at FROM alert_inhibition_active ORDER BY rule_uid,instance_key`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	type sources struct {
		all, oneSided   []model.InhibitionActiveInstance
		count, oneCount int
	}
	index := map[string]*sources{}
	targets := []model.InhibitionActiveInstance{}
	for rows.Next() {
		v, e := scanInhibitionActive(rows)
		if e != nil {
			return out, e
		}
		source, target := inhibitionMatches(c.source, v.Labels), inhibitionMatches(c.target, v.Labels)
		if source {
			key := c.equalKey(v.Labels)
			entry := index[key]
			if entry == nil {
				entry = &sources{all: []model.InhibitionActiveInstance{}, oneSided: []model.InhibitionActiveInstance{}}
				index[key] = entry
			}
			entry.count++
			if len(entry.all) < 20 {
				entry.all = append(entry.all, v)
			}
			if !target {
				entry.oneCount++
				if len(entry.oneSided) < 20 {
					entry.oneSided = append(entry.oneSided, v)
				}
			}
		}
		if target {
			out.TotalTargets++
			if len(targets) < input.Limit {
				targets = append(targets, v)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	match := func(labels map[string]string) ([]model.InhibitionActiveInstance, int, bool) {
		entry := index[c.equalKey(labels)]
		excluded := false
		empty := []model.InhibitionActiveInstance{}
		if entry == nil {
			return empty, 0, false
		}
		if inhibitionMatches(c.source, labels) {
			excluded = entry.count > entry.oneCount
			return entry.oneSided, entry.oneCount, excluded
		}
		return entry.all, entry.count, false
	}
	if input.Labels != nil {
		out.SourceMatches = inhibitionMatches(c.source, *input.Labels)
		out.TargetMatches = inhibitionMatches(c.target, *input.Labels)
		if out.TargetMatches {
			out.Sources, out.SourceCount, out.ExcludedSelfMatch = match(*input.Labels)
			out.Inhibited = out.SourceCount > 0 && !input.Disabled
		}
	}
	for _, v := range targets {
		sources, count, excluded := match(v.Labels)
		out.Targets = append(out.Targets, model.InhibitionPreviewTarget{Target: v, Inhibited: count > 0 && !input.Disabled, ExcludedSelfMatch: excluded, Sources: sources, SourceCount: count})
	}
	out.Truncated = out.TotalTargets > len(out.Targets)
	return out, nil
}

package store

import (
	"context"
	"encoding/json"

	"github.com/neko233-com/MetricsPanel233/internal/model"
)

type NotificationGroupView struct {
	Key                     string                         `json:"key"`
	Receiver                string                         `json:"receiver"`
	GroupLabels             map[string]string              `json:"group_labels"`
	Options                 model.NotificationRouteOptions `json:"options"`
	CreatedAt               int64                          `json:"created_at"`
	LastSentAt              int64                          `json:"last_sent_at"`
	NextAt                  int64                          `json:"next_at"`
	InflightID              int64                          `json:"inflight_id"`
	ResumeNeeded            bool                           `json:"resume_needed"`
	Firing                  int                            `json:"firing"`
	Resolved                int                            `json:"resolved"`
	EffectiveRepeatInterval string                         `json:"effective_repeat_interval"`
}

func (s *Store) NotificationGroups(ctx context.Context) ([]NotificationGroupView, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT `+groupColumns+` FROM alert_notification_groups WHERE EXISTS(SELECT 1 FROM alert_notification_group_members m WHERE m.group_key=alert_notification_groups.group_key) OR inflight_id>0 ORDER BY created_at DESC LIMIT 500`)
	if err != nil {
		return nil, err
	}
	groups := []notificationGroup{}
	for rows.Next() {
		g, err := scanNotificationGroup(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		groups = append(groups, g)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []NotificationGroupView{}
	for _, g := range groups {
		v := NotificationGroupView{Key: g.Key, Receiver: g.Receiver, CreatedAt: g.CreatedAt, LastSentAt: g.LastSentAt, NextAt: g.NextAt, InflightID: g.InflightID, ResumeNeeded: g.ResumeNeeded}
		if err := json.Unmarshal([]byte(g.Options), &v.Options); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(g.Labels), &v.GroupLabels); err != nil {
			return nil, err
		}
		v.EffectiveRepeatInterval = effectiveRepeat(v.Options).String()
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM alert_notification_group_members WHERE group_key=? AND status='firing'`, g.Key).Scan(&v.Firing); err != nil {
			return nil, err
		}
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM alert_notification_group_members WHERE group_key=? AND status='resolved'`, g.Key).Scan(&v.Resolved); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

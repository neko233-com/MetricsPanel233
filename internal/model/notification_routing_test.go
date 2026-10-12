package model

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNotificationRecursiveInheritanceContinueAndGroupLabels(t *testing.T) {
	labels := []string{"team"}
	p := NotificationPolicy{Receiver: "default", NotificationTiming: NotificationTiming{GroupBy: &labels, GroupWait: "30s", GroupInterval: "5m", RepeatInterval: "4h"}, Routes: []NotificationRoute{
		{Receiver: "ops", Continue: true, MuteTimeIntervals: []string{"parent-mute"}, Matchers: []NotificationMatcher{{Name: "team", Operator: "=", Value: "ops"}}, Routes: []NotificationRoute{{Matchers: []NotificationMatcher{{Name: "severity", Operator: "=", Value: "critical"}}, NotificationTiming: NotificationTiming{GroupWait: "1s"}}}},
		{Receiver: "audit", Matchers: []NotificationMatcher{{Name: "team", Operator: "=", Value: "ops"}}},
	}}
	require.NoError(t, p.Validate())
	out := p.MatchingRoutes(map[string]string{"team": "ops", "severity": "critical"})
	require.Len(t, out, 2)
	assert.Equal(t, "ops", out[0].Receiver)
	assert.Equal(t, "1s", out[0].GroupWait)
	assert.Equal(t, "5m", out[0].GroupInterval)
	assert.Empty(t, out[0].MuteTimeIntervals, "route time intervals are local, not inherited")
	assert.Equal(t, "audit", out[1].Receiver)
	assert.Equal(t, "default", p.MatchingRoutes(map[string]string{"team": "other"})[0].Receiver)
	out = p.MatchingRoutes(map[string]string{"team": "ops"})
	assert.Equal(t, []string{"parent-mute"}, out[0].MuteTimeIntervals)
	assert.Equal(t, map[string]string{"team": "ops"}, out[0].GroupLabels(map[string]string{"team": "ops", "instance": "a"}))
	var parsed NotificationPolicy
	require.NoError(t, json.Unmarshal([]byte(`{"receiver":"ops","group_by":[],"group_wait":"0s"}`), &parsed))
	require.NotNil(t, parsed.GroupBy)
	assert.Empty(t, *parsed.GroupBy)
	assert.Empty(t, NotificationRouteOptions{Receiver: "ops"}.GroupKey(nil), "legacy policy uses immediate independent deliveries")
}

func TestMuteTimingDSTCalendarAndRoundtrip(t *testing.T) {
	timing := MuteTiming{Name: "night", TimeIntervals: []json.RawMessage{json.RawMessage(`{"times":[{"start_time":"01:00","end_time":"02:00"}],"location":"America/New_York"}`)}}
	for _, v := range []struct {
		at   string
		want bool
	}{{"2026-11-01T05:30:00Z", true}, {"2026-11-01T06:30:00Z", true}, {"2026-11-01T07:00:00Z", false}, {"2026-03-08T06:30:00Z", true}, {"2026-03-08T07:30:00Z", false}} {
		at, err := time.Parse(time.RFC3339, v.at)
		require.NoError(t, err)
		got, err := timing.Matches(at)
		require.NoError(t, err)
		assert.Equal(t, v.want, got, v.at)
	}
	raw := json.RawMessage(`{"days_of_month":["-1"],"months":["february"],"years":["2028"],"location":"UTC"}`)
	timing.TimeIntervals = []json.RawMessage{raw}
	at, _ := time.Parse(time.RFC3339, "2028-02-29T12:00:00Z")
	got, err := timing.Matches(at)
	require.NoError(t, err)
	assert.True(t, got)
	at = at.Add(-24 * time.Hour)
	got, err = timing.Matches(at)
	require.NoError(t, err)
	assert.False(t, got)
	encoded, err := json.Marshal(timing)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"days_of_month":["-1"]`)
	for _, bad := range []string{`{"location":"Fake/Zone"}`, `{"times":[{"start_time":"25:00","end_time":"26:00"}]}`, `{"unknown":true}`} {
		timing.TimeIntervals = []json.RawMessage{json.RawMessage(bad)}
		_, err = timing.Intervals()
		assert.Error(t, err)
	}
}

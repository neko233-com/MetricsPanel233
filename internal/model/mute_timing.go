package model

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/prometheus/alertmanager/timeinterval"
)

// Keep the upstream interval JSON as raw data. Validation and execution share
// Alertmanager's calendar implementation, while export retains its exact DSL.
type MuteTiming struct {
	Name          string            `json:"name"`
	TimeIntervals []json.RawMessage `json:"time_intervals"`
	Version       int               `json:"version,omitempty"`
	UpdatedAt     int64             `json:"updated_at,omitempty"`
}

func (m MuteTiming) Intervals() ([]timeinterval.TimeInterval, error) {
	if strings.TrimSpace(m.Name) == "" || len(m.Name) > 128 || strings.ContainsAny(m.Name, "\r\n\x00") || len(m.TimeIntervals) < 1 || len(m.TimeIntervals) > 32 {
		return nil, errors.New("mute timing requires a name of 1–128 bytes and 1–32 intervals")
	}
	out := []timeinterval.TimeInterval{}
	allowed := map[string]bool{"times": true, "weekdays": true, "days_of_month": true, "months": true, "years": true, "location": true}
	for _, raw := range m.TimeIntervals {
		var object map[string]json.RawMessage
		if len(raw) > 16384 || json.Unmarshal(raw, &object) != nil || object == nil {
			return nil, errors.New("invalid mute timing interval JSON")
		}
		for field := range object {
			if !allowed[field] {
				return nil, errors.New("unsupported mute timing interval field: " + field)
			}
		}
		if rawTimes, ok := object["times"]; ok {
			var ranges []map[string]json.RawMessage
			if err := json.Unmarshal(rawTimes, &ranges); err != nil {
				return nil, errors.New("times must be an array of start_time/end_time ranges")
			}
			for _, r := range ranges {
				for key := range r {
					if key != "start_time" && key != "end_time" {
						return nil, errors.New("unsupported mute time range field: " + key)
					}
				}
				if len(r["start_time"]) == 0 || len(r["end_time"]) == 0 {
					return nil, errors.New("time range requires start_time and end_time")
				}
			}
		}
		var interval timeinterval.TimeInterval
		if err := json.Unmarshal(raw, &interval); err != nil {
			return nil, err
		}
		out = append(out, interval)
	}
	return out, nil
}

func (m MuteTiming) Matches(at time.Time) (bool, error) {
	intervals, err := m.Intervals()
	if err != nil {
		return false, err
	}
	for _, interval := range intervals {
		if interval.ContainsTime(at) {
			return true, nil
		}
	}
	return false, nil
}

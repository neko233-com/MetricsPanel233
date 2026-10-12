package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	commonmodel "github.com/prometheus/common/model"
)

type NotificationTiming struct {
	GroupBy        *[]string `json:"group_by,omitempty"`
	GroupWait      string    `json:"group_wait,omitempty"`
	GroupInterval  string    `json:"group_interval,omitempty"`
	RepeatInterval string    `json:"repeat_interval,omitempty"`
}

func NotificationDuration(value string) (time.Duration, error) {
	if value == "" {
		return 0, nil
	}
	duration, err := commonmodel.ParseDuration(value)
	if err != nil || duration < 0 || time.Duration(duration) > 366*24*time.Hour {
		return 0, errors.New("notification duration must be a nonnegative duration no greater than 366 days")
	}
	return time.Duration(duration), nil
}

func (t NotificationTiming) Validate() error {
	for _, value := range []string{t.GroupWait, t.GroupInterval, t.RepeatInterval} {
		if _, err := NotificationDuration(value); err != nil {
			return err
		}
	}
	if t.GroupInterval != "" {
		value, _ := NotificationDuration(t.GroupInterval)
		if value == 0 {
			return errors.New("group_interval must be greater than zero")
		}
	}
	if t.GroupBy != nil {
		if len(*t.GroupBy) > 32 {
			return errors.New("at most 32 group_by labels")
		}
		seen := map[string]bool{}
		for _, name := range *t.GroupBy {
			if name == "" || len(name) > 128 || strings.ContainsAny(name, "\r\n\x00") || seen[name] || (name == "..." && len(*t.GroupBy) != 1) {
				return errors.New("group_by requires unique label names, or ... alone to group by all labels")
			}
			seen[name] = true
		}
	}
	return nil
}

func validateNotificationRoutes(routes []NotificationRoute, inherited string, depth int, count *int) error {
	if depth > 8 {
		return errors.New("notification routes support at most 8 nested levels")
	}
	for _, route := range routes {
		*count++
		if *count > 128 || len(route.Matchers) > 32 {
			return errors.New("notification policy supports at most 128 routes and 32 matchers per route")
		}
		receiver := inherited
		if route.Receiver != "" {
			if !alertUID.MatchString(route.Receiver) {
				return errors.New("invalid notification receiver UID")
			}
			receiver = route.Receiver
		}
		if receiver == "" && len(route.Routes) == 0 {
			return errors.New("notification route needs an explicit or inherited receiver")
		}
		for _, matcher := range route.Matchers {
			if err := matcher.Validate(); err != nil {
				return err
			}
		}
		if err := route.NotificationTiming.Validate(); err != nil {
			return err
		}
		for _, names := range [][]string{route.MuteTimeIntervals, route.ActiveTimeIntervals} {
			if len(names) > 32 {
				return errors.New("at most 32 time intervals per route")
			}
			for _, name := range names {
				if strings.TrimSpace(name) == "" || len(name) > 128 {
					return errors.New("invalid notification time interval name")
				}
			}
		}
		if err := validateNotificationRoutes(route.Routes, receiver, depth+1, count); err != nil {
			return err
		}
	}
	return nil
}

type NotificationRouteOptions struct {
	NotificationTiming
	Receiver            string   `json:"receiver"`
	RouteKey            string   `json:"route_key"`
	MuteTimeIntervals   []string `json:"mute_time_intervals,omitempty"`
	ActiveTimeIntervals []string `json:"active_time_intervals,omitempty"`
}

func inheritNotificationTiming(parent, child NotificationTiming) NotificationTiming {
	if child.GroupBy != nil {
		parent.GroupBy = child.GroupBy
	}
	if child.GroupWait != "" {
		parent.GroupWait = child.GroupWait
	}
	if child.GroupInterval != "" {
		parent.GroupInterval = child.GroupInterval
	}
	if child.RepeatInterval != "" {
		parent.RepeatInterval = child.RepeatInterval
	}
	return parent
}

func (p NotificationPolicy) MatchingRoutes(labels map[string]string) []NotificationRouteOptions {
	root := NotificationRouteOptions{NotificationTiming: p.NotificationTiming, Receiver: p.Receiver, RouteKey: "root"}
	var match func([]NotificationRoute, NotificationRouteOptions) []NotificationRouteOptions
	match = func(routes []NotificationRoute, parent NotificationRouteOptions) []NotificationRouteOptions {
		out := []NotificationRouteOptions{}
		for index, route := range routes {
			matched := true
			for _, matcher := range route.Matchers {
				if !matcher.Matches(labels) {
					matched = false
					break
				}
			}
			if !matched {
				continue
			}
			opts := NotificationRouteOptions{NotificationTiming: inheritNotificationTiming(parent.NotificationTiming, route.NotificationTiming), Receiver: parent.Receiver, RouteKey: fmt.Sprintf("%s/%d", parent.RouteKey, index), MuteTimeIntervals: route.MuteTimeIntervals, ActiveTimeIntervals: route.ActiveTimeIntervals}
			if route.Receiver != "" {
				opts.Receiver = route.Receiver
			}
			children := match(route.Routes, opts)
			if len(children) == 0 {
				out = append(out, opts)
			} else {
				out = append(out, children...)
			}
			if !route.Continue {
				break
			}
		}
		return out
	}
	if out := match(p.Routes, root); len(out) > 0 {
		return out
	}
	return []NotificationRouteOptions{root}
}

func (r NotificationRouteOptions) GroupLabels(labels map[string]string) map[string]string {
	out := map[string]string{}
	if r.GroupBy == nil {
		return out
	}
	for _, name := range *r.GroupBy {
		if name == "..." {
			for key, value := range labels {
				out[key] = value
			}
			return out
		}
		if value, ok := labels[name]; ok {
			out[name] = value
		}
	}
	return out
}

func (r NotificationRouteOptions) GroupKey(labels map[string]string) string {
	if r.GroupBy == nil && r.GroupWait == "" && r.GroupInterval == "" && r.RepeatInterval == "" && len(r.MuteTimeIntervals) == 0 && len(r.ActiveTimeIntervals) == 0 {
		return ""
	}
	groupLabels := r.GroupLabels(labels)
	if r.GroupBy == nil && r.GroupWait == "" && r.GroupInterval == "" && r.RepeatInterval == "" {
		groupLabels = labels
	}
	raw, _ := json.Marshal([]any{r.Receiver, r.RouteKey, groupLabels})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

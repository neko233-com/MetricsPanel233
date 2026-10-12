package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/neko233-com/MetricsPanel233/internal/alerting"
	"github.com/neko233-com/MetricsPanel233/internal/model"
)

func (s *Server) exportNotificationRule(ctx context.Context, rule model.AlertRule) (alerting.GrafanaRule, error) {
	g := alerting.ExportGrafana(rule, s.OrganizationID())
	if rule.NotificationReceiver == "" {
		g.NotificationSettings = nil
		return g, nil
	}
	point, err := s.Store.ContactPoint(ctx, rule.NotificationReceiver)
	if err != nil {
		return g, err
	}
	g.NotificationSettings, err = json.Marshal(map[string]string{"receiver": point.Name})
	return g, err
}

func (s *Server) exportNotificationGroup(ctx context.Context, folder, group string, interval int, views []model.AlertRuleView) (alerting.GrafanaGroup, error) {
	out := alerting.GrafanaGroup{Title: group, FolderUID: folder, Interval: interval, Rules: []alerting.GrafanaRule{}}
	for _, view := range views {
		g, err := s.exportNotificationRule(ctx, view.AlertRule)
		if err != nil {
			return out, err
		}
		out.Rules = append(out.Rules, g)
	}
	return out, nil
}

// These provisioning endpoints deliberately reject notification settings whose
// execution is unavailable rather than importing a route that cannot deliver.
type provisionedContactPoint struct {
	UID                   string                     `json:"uid"`
	Name                  string                     `json:"name"`
	Type                  string                     `json:"type"`
	Settings              map[string]json.RawMessage `json:"settings"`
	DisableResolveMessage bool                       `json:"disableResolveMessage"`
	Provenance            string                     `json:"provenance,omitempty"`
	Version               int                        `json:"version,omitempty"`
}

func exportProvisionedPoint(point model.ContactPoint) provisionedContactPoint {
	settings := map[string]json.RawMessage{}
	settings["url"], _ = json.Marshal(point.URL)
	settings["httpMethod"], _ = json.Marshal("POST")
	if point.TitleTemplate != "" {
		settings["title"], _ = json.Marshal(point.TitleTemplate)
	}
	if point.MessageTemplate != "" {
		settings["message"], _ = json.Marshal(point.MessageTemplate)
	}
	if point.SecureFields["bearer_token"] {
		settings["authorization_scheme"], _ = json.Marshal("Bearer")
		settings["authorization_credentials"], _ = json.Marshal("[REDACTED]")
	}
	return provisionedContactPoint{UID: point.UID, Name: point.Name, Type: point.Type, Settings: settings, DisableResolveMessage: point.DisableResolveMessage, Provenance: "api", Version: point.Version}
}

func compileProvisionedPoint(point provisionedContactPoint) (model.ContactPointInput, error) {
	input := model.ContactPointInput{ContactPoint: model.ContactPoint{UID: point.UID, Name: point.Name, Type: point.Type, DisableResolveMessage: point.DisableResolveMessage, Version: point.Version}}
	for key := range point.Settings {
		switch key {
		case "url", "httpMethod", "authorization_scheme", "authorization_credentials", "title", "message":
		default:
			return input, fmt.Errorf("webhook setting %q is not supported", key)
		}
	}
	readString := func(key string) (string, error) {
		if len(point.Settings[key]) == 0 {
			return "", nil
		}
		var value string
		if err := json.Unmarshal(point.Settings[key], &value); err != nil {
			return "", fmt.Errorf("webhook setting %s must be a string", key)
		}
		return value, nil
	}
	var err error
	if input.URL, err = readString("url"); err != nil {
		return input, err
	}
	if input.TitleTemplate, err = readString("title"); err != nil {
		return input, err
	}
	if input.MessageTemplate, err = readString("message"); err != nil {
		return input, err
	}
	method, err := readString("httpMethod")
	if err != nil || (method != "" && method != http.MethodPost) {
		return input, errors.New("webhook provisioning currently supports POST only")
	}
	scheme, err := readString("authorization_scheme")
	if err != nil || (scheme != "" && !strings.EqualFold(scheme, "Bearer")) {
		return input, errors.New("webhook provisioning currently supports Bearer authentication only")
	}
	token, err := readString("authorization_credentials")
	if err != nil {
		return input, err
	}
	if _, supplied := point.Settings["authorization_credentials"]; supplied && token != "[REDACTED]" {
		input.BearerToken = &token
	}
	return input, nil
}

type provisionedNotificationRoute struct {
	model.NotificationTiming
	Receiver            string                         `json:"receiver"`
	ObjectMatchers      [][]string                     `json:"object_matchers,omitempty"`
	Continue            bool                           `json:"continue,omitempty"`
	Routes              []provisionedNotificationRoute `json:"routes,omitempty"`
	Provenance          string                         `json:"provenance,omitempty"`
	Version             int                            `json:"version,omitempty"`
	MuteTimeIntervals   []string                       `json:"mute_time_intervals,omitempty"`
	ActiveTimeIntervals []string                       `json:"active_time_intervals,omitempty"`
}

func exportProvisionedRoute(route model.NotificationRoute, name func(string) string) provisionedNotificationRoute {
	out := provisionedNotificationRoute{NotificationTiming: route.NotificationTiming, Receiver: name(route.Receiver), Continue: route.Continue, MuteTimeIntervals: route.MuteTimeIntervals, ActiveTimeIntervals: route.ActiveTimeIntervals}
	for _, m := range route.Matchers {
		out.ObjectMatchers = append(out.ObjectMatchers, []string{m.Name, m.Operator, m.Value})
	}
	for _, child := range route.Routes {
		out.Routes = append(out.Routes, exportProvisionedRoute(child, name))
	}
	return out
}

func compileProvisionedRoute(route provisionedNotificationRoute, points []model.ContactPoint, depth int) (model.NotificationRoute, error) {
	out := model.NotificationRoute{NotificationTiming: route.NotificationTiming, Continue: route.Continue, MuteTimeIntervals: route.MuteTimeIntervals, ActiveTimeIntervals: route.ActiveTimeIntervals}
	if depth > 8 {
		return out, errors.New("notification routes support at most 8 nested levels")
	}
	var err error
	out.Receiver, err = resolveProvisionedReceiver(points, route.Receiver)
	if err != nil {
		return out, err
	}
	for _, m := range route.ObjectMatchers {
		if len(m) != 3 {
			return out, errors.New("object matcher requires [label,operator,value]")
		}
		out.Matchers = append(out.Matchers, model.NotificationMatcher{Name: m[0], Operator: m[1], Value: m[2]})
	}
	for _, child := range route.Routes {
		v, err := compileProvisionedRoute(child, points, depth+1)
		if err != nil {
			return out, err
		}
		out.Routes = append(out.Routes, v)
	}
	return out, nil
}

func resolveProvisionedReceiver(points []model.ContactPoint, receiver string) (string, error) {
	if receiver == "" {
		return "", nil
	}
	for _, point := range points {
		if point.UID == receiver {
			return point.UID, nil
		}
	}
	matches := []string{}
	for _, point := range points {
		if point.Name == receiver {
			matches = append(matches, point.UID)
		}
	}
	if len(matches) != 1 {
		return "", errors.New("receiver must identify one existing contact point; use its UID for duplicate names")
	}
	return matches[0], nil
}

func (s *Server) notificationProvisioningRoutes(api routeRegistrar) {
	api.HandleFunc("GET /api/v1/provisioning/contact-points", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("decrypt") == "true" {
			fail(w, 400, errors.New("contact point credentials are write-only"))
			return
		}
		points, err := s.Store.ContactPoints(r.Context())
		if err != nil {
			fail(w, 500, err)
			return
		}
		out := []provisionedContactPoint{}
		for _, point := range points {
			if name := r.URL.Query().Get("name"); name == "" || point.Name == name {
				out = append(out, exportProvisionedPoint(point))
			}
		}
		writeJSON(w, 200, out)
	})
	save := func(w http.ResponseWriter, r *http.Request) {
		var point provisionedContactPoint
		var previous *model.ContactPoint
		if err := decode(w, r, &point); err != nil {
			fail(w, 400, err)
			return
		}
		if uid := r.PathValue("uid"); uid != "" {
			if point.UID != "" && point.UID != uid {
				fail(w, 400, errors.New("contact point UID does not match the URL"))
				return
			}
			current, err := s.Store.ContactPoint(r.Context(), uid)
			if err != nil {
				notificationError(w, err)
				return
			}
			point.UID = uid
			previous = &current
			if point.Version == 0 {
				point.Version = current.Version
			}
		} else if point.Version != 0 {
			fail(w, 409, errors.New("create contact points with version zero"))
			return
		}
		input, err := compileProvisionedPoint(point)
		if err != nil {
			fail(w, 400, err)
			return
		}
		if previous != nil {
			input.Disabled = previous.Disabled
			input.TimeoutSeconds = previous.TimeoutSeconds
			if _, present := point.Settings["title"]; !present {
				input.TitleTemplate = previous.TitleTemplate
			}
			if _, present := point.Settings["message"]; !present {
				input.MessageTemplate = previous.MessageTemplate
			}
		}
		value, err := s.Store.SaveContactPoint(r.Context(), input)
		if err != nil {
			notificationError(w, err)
			return
		}
		writeJSON(w, 202, exportProvisionedPoint(value))
	}
	api.HandleFunc("POST /api/v1/provisioning/contact-points", save)
	api.HandleFunc("PUT /api/v1/provisioning/contact-points/{uid}", save)
	api.HandleFunc("DELETE /api/v1/provisioning/contact-points/{uid}", func(w http.ResponseWriter, r *http.Request) {
		if err := s.Store.DeleteContactPoint(r.Context(), r.PathValue("uid")); err != nil {
			notificationError(w, err)
			return
		}
		writeJSON(w, 202, map[string]string{"message": "contact point deleted"})
	})
	api.HandleFunc("GET /api/v1/provisioning/policies", func(w http.ResponseWriter, r *http.Request) {
		p, err := s.Store.NotificationPolicy(r.Context())
		if err != nil {
			fail(w, 500, err)
			return
		}
		points, err := s.Store.ContactPoints(r.Context())
		if err != nil {
			fail(w, 500, err)
			return
		}
		name := func(uid string) string {
			for _, point := range points {
				if point.UID == uid {
					return point.Name
				}
			}
			return uid
		}
		out := provisionedNotificationRoute{NotificationTiming: p.NotificationTiming, Receiver: name(p.Receiver), Provenance: "api", Version: p.Version}
		for _, route := range p.Routes {
			out.Routes = append(out.Routes, exportProvisionedRoute(route, name))
		}
		writeJSON(w, 200, out)
	})
	api.HandleFunc("PUT /api/v1/provisioning/policies", func(w http.ResponseWriter, r *http.Request) {
		var input provisionedNotificationRoute
		if err := decode(w, r, &input); err != nil {
			fail(w, 400, err)
			return
		}
		if len(input.ObjectMatchers) != 0 || input.Continue || len(input.MuteTimeIntervals) > 0 || len(input.ActiveTimeIntervals) > 0 {
			fail(w, 400, errors.New("root notification policy cannot have matchers or continue"))
			return
		}
		points, err := s.Store.ContactPoints(r.Context())
		if err != nil {
			fail(w, 500, err)
			return
		}
		p, err := s.Store.NotificationPolicy(r.Context())
		if err != nil {
			fail(w, 500, err)
			return
		}
		if input.Version != 0 {
			p.Version = input.Version
		}
		if p.Version == 0 {
			if input.GroupWait == "" {
				input.GroupWait = "30s"
			}
			if input.GroupInterval == "" {
				input.GroupInterval = "5m"
			}
			if input.RepeatInterval == "" {
				input.RepeatInterval = "4h"
			}
		} else {
			if input.GroupBy == nil {
				input.GroupBy = p.GroupBy
			}
			if input.GroupWait == "" {
				input.GroupWait = p.GroupWait
			}
			if input.GroupInterval == "" {
				input.GroupInterval = p.GroupInterval
			}
			if input.RepeatInterval == "" {
				input.RepeatInterval = p.RepeatInterval
			}
		}
		p.NotificationTiming = input.NotificationTiming
		p.Receiver, err = resolveProvisionedReceiver(points, input.Receiver)
		if err != nil {
			fail(w, 400, err)
			return
		}
		p.Routes = []model.NotificationRoute{}
		for _, route := range input.Routes {
			item, err := compileProvisionedRoute(route, points, 1)
			if err != nil {
				fail(w, 400, err)
				return
			}
			p.Routes = append(p.Routes, item)
		}
		if _, err := s.Store.SaveNotificationPolicy(r.Context(), p); err != nil {
			notificationError(w, err)
			return
		}
		writeJSON(w, 202, map[string]string{"message": "notification policy updated"})
	})
	api.HandleFunc("DELETE /api/v1/provisioning/policies", func(w http.ResponseWriter, r *http.Request) {
		p, err := s.Store.NotificationPolicy(r.Context())
		if err != nil {
			fail(w, 500, err)
			return
		}
		p.Receiver, p.Routes = "", []model.NotificationRoute{}
		p.NotificationTiming = model.NotificationTiming{}
		if _, err := s.Store.SaveNotificationPolicy(r.Context(), p); err != nil {
			notificationError(w, err)
			return
		}
		writeJSON(w, 202, map[string]string{"message": "notification policy reset"})
	})
}

package server

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/notificationtemplate"
	"github.com/neko233-com/MetricsPanel233/internal/store"
)

type provisionedNotificationTemplate struct {
	Name       string `json:"name"`
	Template   string `json:"template"`
	Version    string `json:"version,omitempty"`
	Provenance string `json:"provenance,omitempty"`
}

func exportNotificationTemplate(value model.NotificationTemplate) provisionedNotificationTemplate {
	return provisionedNotificationTemplate{Name: value.Name, Template: value.Template, Version: strconv.Itoa(value.Version), Provenance: "api"}
}

func (s *Server) notificationTemplateRoutes(api routeRegistrar) {
	grafana := func(r *http.Request) bool { return strings.HasPrefix(r.URL.Path, "/api/v1/provisioning/") }
	list := func(w http.ResponseWriter, r *http.Request) {
		values, err := s.Store.NotificationTemplates(r.Context())
		if err != nil {
			notificationError(w, err)
			return
		}
		if grafana(r) {
			out := []provisionedNotificationTemplate{}
			for _, v := range values {
				out = append(out, exportNotificationTemplate(v))
			}
			writeJSON(w, 200, out)
		} else {
			writeJSON(w, 200, values)
		}
	}
	get := func(w http.ResponseWriter, r *http.Request) {
		value, err := s.Store.NotificationTemplate(r.Context(), r.PathValue("name"))
		if err != nil {
			notificationError(w, err)
			return
		}
		if grafana(r) {
			writeJSON(w, 200, exportNotificationTemplate(value))
		} else {
			writeJSON(w, 200, value)
		}
	}
	save := func(w http.ResponseWriter, r *http.Request) {
		var value model.NotificationTemplate
		versionProvided := false
		if grafana(r) {
			var input provisionedNotificationTemplate
			if err := decode(w, r, &input); err != nil {
				fail(w, 400, err)
				return
			}
			value.Name, value.Template = input.Name, input.Template
			if input.Version != "" {
				versionProvided = true
				var err error
				value.Version, err = strconv.Atoi(input.Version)
				if err != nil {
					fail(w, 400, errors.New("invalid notification template version"))
					return
				}
			}
		} else if err := decode(w, r, &value); err != nil {
			fail(w, 400, err)
			return
		}
		if name := r.PathValue("name"); name != "" {
			if value.Name != "" && value.Name != name {
				fail(w, 400, errors.New("notification template name does not match URL"))
				return
			}
			value.Name = name
			if grafana(r) && !versionProvided {
				existing, err := s.Store.NotificationTemplate(r.Context(), name)
				if err == nil {
					value.Version = existing.Version
				} else if !errors.Is(err, sql.ErrNoRows) {
					notificationError(w, err)
					return
				}
			}
			if !grafana(r) && value.Version == 0 {
				notificationError(w, store.ErrNotificationConflict)
				return
			}
		} else if value.Version != 0 {
			notificationError(w, store.ErrNotificationConflict)
			return
		}
		value, err := s.Store.SaveNotificationTemplate(r.Context(), value)
		if err != nil {
			notificationError(w, err)
			return
		}
		if grafana(r) {
			writeJSON(w, 202, exportNotificationTemplate(value))
		} else {
			code := 200
			if r.Method == http.MethodPost {
				code = 201
			}
			writeJSON(w, code, value)
		}
	}
	remove := func(w http.ResponseWriter, r *http.Request) {
		var version *int
		if raw := r.URL.Query().Get("version"); raw != "" {
			v, err := strconv.Atoi(raw)
			if err != nil || v < 1 {
				fail(w, 400, errors.New("invalid template version"))
				return
			}
			version = &v
		}
		if err := s.Store.DeleteNotificationTemplate(r.Context(), r.PathValue("name"), version); err != nil {
			notificationError(w, err)
			return
		}
		code := 200
		if grafana(r) {
			code = 202
		}
		writeJSON(w, code, map[string]bool{"deleted": true})
	}
	for _, base := range []string{"/api/v1/alerting/notification-templates", "/api/v1/provisioning/templates"} {
		api.HandleFunc("GET "+base, list)
		api.HandleFunc("GET "+base+"/{name}", get)
		api.HandleFunc("PUT "+base+"/{name}", save)
		api.HandleFunc("DELETE "+base+"/{name}", remove)
	}
	api.HandleFunc("POST /api/v1/alerting/notification-templates", save)
	api.HandleFunc("POST /api/v1/alerting/notification-templates/preview", s.notificationTemplatePreview)
}

type templatePreviewInput struct {
	Name     string                     `json:"name,omitempty"`
	Template string                     `json:"template,omitempty"`
	Data     *notificationtemplate.Data `json:"data,omitempty"`
	Title    string                     `json:"title,omitempty"`
	Message  string                     `json:"message,omitempty"`
}
type templatePreviewValue struct {
	Name string `json:"name"`
	Text string `json:"text"`
}
type templatePreviewError struct {
	Name  string `json:"name"`
	Error string `json:"error"`
}

func (s *Server) notificationTemplatePreview(w http.ResponseWriter, r *http.Request) {
	var input templatePreviewInput
	if err := decode(w, r, &input); err != nil {
		fail(w, 400, err)
		return
	}
	values, err := s.Store.NotificationTemplates(r.Context())
	if err != nil {
		notificationError(w, err)
		return
	}
	definitions := []notificationtemplate.Definition{}
	if input.Template != "" && input.Name == "" {
		input.Name = "preview"
		for _, value := range values {
			if value.Name == input.Name {
				input.Name += "_"
			}
		}
	}
	found := false
	for _, v := range values {
		if v.Name == input.Name {
			found = true
			if input.Template != "" {
				v.Template = input.Template
			}
		}
		definitions = append(definitions, notificationtemplate.Definition{Name: v.Name, Template: v.Template, Version: v.Version})
	}
	if input.Template != "" && !found {
		if input.Name == "" {
			input.Name = "preview"
		}
		definitions = append(definitions, notificationtemplate.Definition{Name: input.Name, Template: input.Template})
	} else if input.Name != "" && !found {
		notificationError(w, sql.ErrNoRows)
		return
	}
	compiled, err := notificationtemplate.Compile(definitions)
	if err != nil {
		notificationError(w, err)
		return
	}
	data := notificationtemplate.Data{Receiver: "Preview", Status: "firing", GroupLabels: notificationtemplate.KV{"alertname": "Example alert"}, CommonLabels: notificationtemplate.KV{"alertname": "Example alert", "instance": "server-01"}, CommonAnnotations: notificationtemplate.KV{"summary": "Example notification"}, Alerts: notificationtemplate.Alerts{{Status: "firing", Labels: notificationtemplate.KV{"alertname": "Example alert", "instance": "server-01"}, Annotations: notificationtemplate.KV{"summary": "Example notification"}, StartsAt: time.Now().Add(-5 * time.Minute), Values: map[string]float64{"A": 1}}}}
	if input.Data != nil {
		data = *input.Data
	}
	if data.OrgID == 0 {
		data.OrgID = s.OrganizationID()
	}
	if err := data.Normalize(time.Now()); err != nil {
		notificationError(w, err)
		return
	}
	expressions := map[string]string{}
	if input.Title != "" {
		expressions["title"] = input.Title
	}
	if input.Message != "" {
		expressions["message"] = input.Message
	}
	if len(expressions) == 0 {
		if input.Name != "" {
			for _, name := range compiled.Names[input.Name] {
				expressions[name] = "{{template " + strconv.Quote(name) + " .}}"
			}
		} else {
			fail(w, 400, errors.New("preview requires template group name or title/message expression"))
			return
		}
	}
	results, failures := []templatePreviewValue{}, []templatePreviewError{}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	total := 0
	for _, name := range sortedTemplateExpressions(expressions) {
		output, err := compiled.Render(ctx, expressions[name], data)
		if err != nil {
			failures = append(failures, templatePreviewError{Name: name, Error: err.Error()})
			continue
		}
		total += len(output)
		if total > notificationtemplate.MaxOutput {
			failures = append(failures, templatePreviewError{Name: name, Error: notificationtemplate.ErrLimit.Error()})
			break
		}
		results = append(results, templatePreviewValue{Name: name, Text: output})
	}
	writeJSON(w, 200, map[string]any{"results": results, "errors": failures})
}
func sortedTemplateExpressions(values map[string]string) []string {
	out := []string{}
	for name := range values {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

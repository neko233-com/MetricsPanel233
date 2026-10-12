package server

import (
	"database/sql"
	"errors"
	"fmt"
	"hash/fnv"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/neko233-com/MetricsPanel233/internal/identity"
	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/store"
)

type grafanaInhibitionMatcher struct {
	Label string `json:"label"`
	Type  string `json:"type"`
	Value string `json:"value"`
}
type grafanaInhibitionSpec struct {
	Source []grafanaInhibitionMatcher `json:"source_matchers"`
	Target []grafanaInhibitionMatcher `json:"target_matchers"`
	Equal  []string                   `json:"equal"`
}
type grafanaInhibitionResource struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name            string            `json:"name"`
		Namespace       string            `json:"namespace"`
		ResourceVersion string            `json:"resourceVersion,omitempty"`
		Annotations     map[string]string `json:"annotations,omitempty"`
	} `json:"metadata"`
	Spec grafanaInhibitionSpec `json:"spec"`
}

const inhibitionGroupVersion = "notifications.alerting.grafana.app/v1beta1"
const inhibitionDisplayAnnotation = "metricspanel233.io/display-name"
const inhibitionDisabledAnnotation = "metricspanel233.io/disabled"
const inhibitionNativeVersionAnnotation = "metricspanel233.io/native-version"

// Grafana versions the matcher/equal content using an order-independent FNV64a
// fingerprint. Native writes additionally require the monotonic integer CAS.
func inhibitionResourceVersion(v model.InhibitionRule) string {
	h := fnv.New64a()
	sep := []byte{255}
	for _, side := range [][]model.NotificationMatcher{v.SourceMatchers, v.TargetMatchers} {
		copySide := append([]model.NotificationMatcher{}, side...)
		sort.Slice(copySide, func(i, j int) bool {
			a, b := copySide[i], copySide[j]
			if a.Operator != b.Operator {
				return a.Operator < b.Operator
			}
			if a.Name != b.Name {
				return a.Name < b.Name
			}
			return a.Value < b.Value
		})
		for _, m := range copySide {
			for _, value := range []string{m.Operator, m.Name, m.Value} {
				_, _ = h.Write([]byte(value))
				_, _ = h.Write(sep)
			}
		}
	}
	equal := append([]string{}, v.Equal...)
	sort.Strings(equal)
	for _, name := range equal {
		_, _ = h.Write([]byte(name))
		_, _ = h.Write(sep)
	}
	return fmt.Sprintf("%016x", h.Sum64())
}
func grafanaInhibition(v model.InhibitionRule, namespace string) grafanaInhibitionResource {
	var out grafanaInhibitionResource
	out.APIVersion = inhibitionGroupVersion
	out.Kind = "InhibitionRule"
	out.Metadata.Name = v.UID
	out.Metadata.Namespace = namespace
	out.Metadata.ResourceVersion = inhibitionResourceVersion(v)
	out.Metadata.Annotations = map[string]string{inhibitionDisplayAnnotation: v.Name, inhibitionDisabledAnnotation: strconv.FormatBool(v.Disabled), inhibitionNativeVersionAnnotation: strconv.Itoa(v.Version)}
	out.Spec.Source = []grafanaInhibitionMatcher{}
	out.Spec.Target = []grafanaInhibitionMatcher{}
	out.Spec.Equal = v.Equal
	for _, m := range v.SourceMatchers {
		out.Spec.Source = append(out.Spec.Source, grafanaInhibitionMatcher{Label: m.Name, Type: m.Operator, Value: m.Value})
	}
	for _, m := range v.TargetMatchers {
		out.Spec.Target = append(out.Spec.Target, grafanaInhibitionMatcher{Label: m.Name, Type: m.Operator, Value: m.Value})
	}
	return out
}
func (s *Server) inhibitionRoutes(api routeRegistrar) {
	base := "/api/v1/alerting/inhibition-rules"
	api.HandleFunc("GET "+base, func(w http.ResponseWriter, r *http.Request) {
		v, err := s.Store.InhibitionRules(r.Context())
		if err != nil {
			notificationError(w, err)
			return
		}
		writeJSON(w, 200, v)
	})
	api.HandleFunc("GET "+base+"/active", func(w http.ResponseWriter, r *http.Request) {
		limit := 200
		if raw := r.URL.Query().Get("limit"); raw != "" {
			var err error
			limit, err = strconv.Atoi(raw)
			if err != nil {
				fail(w, 400, errors.New("invalid active instance limit"))
				return
			}
		}
		v, err := s.Store.InhibitionActive(r.Context(), limit)
		if err != nil {
			notificationError(w, err)
			return
		}
		writeJSON(w, 200, v)
	})
	api.HandleFunc("POST "+base+"/preview", func(w http.ResponseWriter, r *http.Request) {
		var v model.InhibitionPreviewInput
		if err := decode(w, r, &v); err != nil {
			fail(w, 400, err)
			return
		}
		out, err := s.Store.PreviewInhibition(r.Context(), v)
		if err != nil {
			notificationError(w, err)
			return
		}
		writeJSON(w, 200, out)
	})
	api.HandleFunc("GET "+base+"/{uid}", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.Store.InhibitionRule(r.Context(), r.PathValue("uid"))
		if err != nil {
			notificationError(w, err)
			return
		}
		writeJSON(w, 200, v)
	})
	save := func(w http.ResponseWriter, r *http.Request) {
		var v model.InhibitionRule
		if err := decode(w, r, &v); err != nil {
			fail(w, 400, err)
			return
		}
		if uid := r.PathValue("uid"); uid != "" {
			if v.UID != "" && v.UID != uid {
				fail(w, 400, errors.New("inhibition UID does not match URL"))
				return
			}
			v.UID = uid
			if v.Version <= 0 {
				notificationError(w, store.ErrNotificationConflict)
				return
			}
			if _, err := s.Store.InhibitionRule(r.Context(), uid); err != nil {
				notificationError(w, err)
				return
			}
		} else if v.Version != 0 {
			notificationError(w, store.ErrNotificationConflict)
			return
		}
		out, err := s.Store.SaveInhibitionRule(r.Context(), v)
		if err != nil {
			notificationError(w, err)
			return
		}
		code := 200
		if r.Method == http.MethodPost {
			code = 201
		}
		writeJSON(w, code, out)
	}
	api.HandleFunc("POST "+base, save)
	api.HandleFunc("PUT "+base+"/{uid}", save)
	api.HandleFunc("DELETE "+base+"/{uid}", func(w http.ResponseWriter, r *http.Request) {
		version, err := strconv.Atoi(r.URL.Query().Get("version"))
		if err != nil || version <= 0 {
			notificationError(w, store.ErrNotificationConflict)
			return
		}
		if err := s.Store.DeleteInhibitionRule(r.Context(), r.PathValue("uid"), version); err != nil {
			notificationError(w, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"deleted": true})
	})

	gbase := "/apis/" + inhibitionGroupVersion + "/namespaces/{namespace}/inhibitionrules"
	validNamespace := func(w http.ResponseWriter, r *http.Request) bool {
		if r.PathValue("namespace") != identity.Namespace(s.OrganizationID()) {
			fail(w, 404, sql.ErrNoRows)
			return false
		}
		return true
	}
	api.HandleFunc("GET "+gbase, func(w http.ResponseWriter, r *http.Request) {
		if !validNamespace(w, r) {
			return
		}
		values, err := s.Store.InhibitionRules(r.Context())
		if err != nil {
			notificationError(w, err)
			return
		}
		items := []grafanaInhibitionResource{}
		for _, v := range values {
			items = append(items, grafanaInhibition(v, r.PathValue("namespace")))
		}
		writeJSON(w, 200, map[string]any{"apiVersion": inhibitionGroupVersion, "kind": "InhibitionRuleList", "metadata": map[string]string{}, "items": items})
	})
	api.HandleFunc("GET "+gbase+"/{uid}", func(w http.ResponseWriter, r *http.Request) {
		if !validNamespace(w, r) {
			return
		}
		v, err := s.Store.InhibitionRule(r.Context(), r.PathValue("uid"))
		if err != nil {
			notificationError(w, err)
			return
		}
		writeJSON(w, 200, grafanaInhibition(v, r.PathValue("namespace")))
	})
	gsave := func(w http.ResponseWriter, r *http.Request) {
		if !validNamespace(w, r) {
			return
		}
		var input grafanaInhibitionResource
		if err := decode(w, r, &input); err != nil {
			fail(w, 400, err)
			return
		}
		if input.APIVersion != "" && input.APIVersion != inhibitionGroupVersion || input.Kind != "" && input.Kind != "InhibitionRule" || input.Metadata.Namespace != "" && input.Metadata.Namespace != r.PathValue("namespace") {
			fail(w, 400, errors.New("inhibition resource kind/version/namespace mismatch"))
			return
		}
		v := model.InhibitionRule{UID: input.Metadata.Name, Name: input.Metadata.Name, Equal: input.Spec.Equal}
		for _, m := range input.Spec.Source {
			v.SourceMatchers = append(v.SourceMatchers, model.NotificationMatcher{Name: m.Label, Operator: m.Type, Value: m.Value})
		}
		for _, m := range input.Spec.Target {
			v.TargetMatchers = append(v.TargetMatchers, model.NotificationMatcher{Name: m.Label, Operator: m.Type, Value: m.Value})
		}
		for name, value := range input.Metadata.Annotations {
			switch name {
			case inhibitionDisplayAnnotation:
				v.Name = value
			case inhibitionDisabledAnnotation:
				var err error
				v.Disabled, err = strconv.ParseBool(value)
				if err != nil {
					fail(w, 400, errors.New("disabled annotation must be boolean"))
					return
				}
			case inhibitionNativeVersionAnnotation:
				if _, err := strconv.Atoi(value); err != nil {
					fail(w, 400, errors.New("invalid native inhibition version annotation"))
					return
				}
			default:
				fail(w, 400, errors.New("unsupported inhibition metadata annotation"))
				return
			}
		}
		if uid := r.PathValue("uid"); uid != "" {
			if v.UID != uid {
				fail(w, 400, errors.New("inhibition metadata.name is immutable and must match URL"))
				return
			}
			previous, err := s.Store.InhibitionRule(r.Context(), uid)
			if err != nil {
				notificationError(w, err)
				return
			}
			if input.Metadata.ResourceVersion == "" || input.Metadata.ResourceVersion != inhibitionResourceVersion(previous) {
				notificationError(w, store.ErrNotificationConflict)
				return
			}
			// The Grafana hash versions the spec only. Native extension edits need
			// their own CAS so a stale GET cannot overwrite display/disabled state.
			_, displayProvided := input.Metadata.Annotations[inhibitionDisplayAnnotation]
			_, disabledProvided := input.Metadata.Annotations[inhibitionDisabledAnnotation]
			if displayProvided || disabledProvided {
				nativeVersion, _ := strconv.Atoi(input.Metadata.Annotations[inhibitionNativeVersionAnnotation])
				if nativeVersion != previous.Version {
					notificationError(w, store.ErrNotificationConflict)
					return
				}
			}
			if !displayProvided {
				v.Name = previous.Name
			}
			if !disabledProvided {
				v.Disabled = previous.Disabled
			}
			v.Version = previous.Version
		} else if input.Metadata.ResourceVersion != "" {
			notificationError(w, store.ErrNotificationConflict)
			return
		} else if v.UID == "" {
			fail(w, 400, errors.New("inhibition metadata.name is required"))
			return
		}
		if r.Method == http.MethodPost {
			if version := input.Metadata.Annotations[inhibitionNativeVersionAnnotation]; version != "" && version != "0" {
				notificationError(w, store.ErrNotificationConflict)
				return
			}
		}
		out, err := s.Store.SaveInhibitionRule(r.Context(), v)
		if err != nil {
			notificationError(w, err)
			return
		}
		code := 200
		if r.Method == http.MethodPost {
			code = 201
		}
		writeJSON(w, code, grafanaInhibition(out, r.PathValue("namespace")))
	}
	api.HandleFunc("POST "+gbase, gsave)
	api.HandleFunc("PUT "+gbase+"/{uid}", gsave)
	api.HandleFunc("DELETE "+gbase+"/{uid}", func(w http.ResponseWriter, r *http.Request) {
		if !validNamespace(w, r) {
			return
		}
		previous, err := s.Store.InhibitionRule(r.Context(), r.PathValue("uid"))
		if err != nil {
			notificationError(w, err)
			return
		}
		version := strings.Trim(r.Header.Get("If-Match"), "\"")
		if r.Body != nil && r.ContentLength != 0 {
			var options struct {
				APIVersion    string `json:"apiVersion,omitempty"`
				Kind          string `json:"kind,omitempty"`
				Preconditions struct {
					ResourceVersion string `json:"resourceVersion"`
				} `json:"preconditions"`
			}
			if err := decode(w, r, &options); err != nil {
				fail(w, 400, err)
				return
			}
			if options.APIVersion != "" && options.APIVersion != "v1" || options.Kind != "" && options.Kind != "DeleteOptions" {
				fail(w, 400, errors.New("unsupported deletion options"))
				return
			}
			version = options.Preconditions.ResourceVersion
		}
		if version == "" || version != inhibitionResourceVersion(previous) {
			notificationError(w, store.ErrNotificationConflict)
			return
		}
		if err := s.Store.DeleteInhibitionRule(r.Context(), previous.UID, previous.Version); err != nil {
			notificationError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"apiVersion": "v1", "kind": "Status", "status": "Success", "code": 200})
	})
}

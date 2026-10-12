package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strconv"
	"strings"

	"github.com/neko233-com/MetricsPanel233/internal/model"
)

type annotationQueryEdit struct {
	OriginalIndex *int            `json:"originalIndex,omitempty"`
	Baseline      json.RawMessage `json:"baseline,omitempty"`
	Query         json.RawMessage `json:"query"`
}

func (s *Server) dashboardAnnotationRoutes(api *http.ServeMux) {
	api.HandleFunc("PATCH /api/v1/dashboards/{id}/annotations", func(w http.ResponseWriter, r *http.Request) {
		match := strings.TrimSpace(r.Header.Get("If-Match"))
		if match == "" {
			fail(w, 428, errors.New("If-Match dashboard revision is required"))
			return
		}
		if len(match) >= 2 && match[0] == '"' && match[len(match)-1] == '"' {
			match = match[1 : len(match)-1]
		}
		revision, err := strconv.ParseInt(match, 10, 64)
		if err != nil || revision <= 0 {
			fail(w, 400, errors.New("If-Match must contain a positive dashboard revision"))
			return
		}
		var input struct {
			Queries *[]annotationQueryEdit `json:"queries"`
		}
		if err := decode(w, r, &input); err != nil {
			fail(w, 400, err)
			return
		}
		if input.Queries == nil || len(*input.Queries) > 32 {
			fail(w, 400, errors.New("queries must be an array with at most 32 annotations"))
			return
		}
		ctx, err := dashboardSaveContext(r)
		if err != nil {
			fail(w, 400, err)
			return
		}
		d, err := s.Store.Dashboard(ctx, r.PathValue("id"))
		if err != nil {
			dashboardResourceError(w, err)
			return
		}
		if d.UpdatedAt != revision {
			fail(w, 409, errors.New("dashboard changed; reload before saving annotation queries"))
			return
		}
		d, err = applyDashboardAnnotations(d, *input.Queries)
		if err != nil {
			fail(w, 400, err)
			return
		}
		d, err = s.saveDashboardWithLibraryPanels(ctx, d, &revision)
		if err != nil {
			dashboardResourceError(w, err)
			return
		}
		writeJSON(w, 200, d)
	})
}

func mergeAnnotationQueries(original []json.RawMessage, edits []annotationQueryEdit) ([]json.RawMessage, error) {
	queries := make([]json.RawMessage, 0, len(edits))
	used := map[int]bool{}
	for _, edit := range edits {
		var object rawObject
		if json.Unmarshal(edit.Query, &object) != nil || object == nil {
			return nil, errors.New("annotation query must be a JSON object")
		}
		raw := edit.Query
		if edit.OriginalIndex != nil {
			index := *edit.OriginalIndex
			if index < 0 || index >= len(original) || used[index] {
				return nil, errors.New("annotation original index is absent or reused")
			}
			used[index] = true
			x, xerr := historyJSON(original[index])
			y, yerr := historyJSON(edit.Baseline)
			if xerr != nil || yerr != nil || !reflect.DeepEqual(browserJSON(x), browserJSON(y)) {
				return nil, errors.New("annotation baseline differs from the stored query; reload before editing")
			}
			raw = mergeQueryEdits(original[index], edit.Baseline, edit.Query)
		} else if len(edit.Baseline) > 0 {
			return nil, errors.New("new annotations must not include a baseline")
		}
		queries = append(queries, raw)
	}
	if len(rawValue(queries)) > 128*1024 {
		return nil, errors.New("annotation queries exceed 128 KiB")
	}
	return queries, nil
}

func patchSourceAnnotations(raw json.RawMessage, edits []annotationQueryEdit) (json.RawMessage, error) {
	var doc rawObject
	if json.Unmarshal(raw, &doc) != nil || doc == nil {
		return nil, errors.New("invalid stored Grafana source")
	}
	for _, wrapper := range []string{"dashboard", "spec"} {
		if child, exists := doc[wrapper]; exists {
			changed, err := patchSourceAnnotations(child, edits)
			if err != nil {
				return nil, err
			}
			doc[wrapper] = changed
			return json.Marshal(doc)
		}
	}
	var original []json.RawMessage
	var annotations rawObject
	_, v2 := doc["elements"]
	if v2 {
		if len(doc["annotations"]) > 0 && string(doc["annotations"]) != "null" && json.Unmarshal(doc["annotations"], &original) != nil {
			return nil, errors.New("invalid stored V2 annotations")
		}
	} else {
		if len(doc["annotations"]) > 0 && string(doc["annotations"]) != "null" && json.Unmarshal(doc["annotations"], &annotations) != nil {
			return nil, errors.New("invalid stored Classic annotations")
		}
		if annotations == nil {
			annotations = rawObject{}
		}
		if len(annotations["list"]) > 0 && json.Unmarshal(annotations["list"], &original) != nil {
			return nil, errors.New("invalid stored annotation list")
		}
	}
	queries, err := mergeAnnotationQueries(original, edits)
	if err != nil {
		return nil, err
	}
	if v2 {
		doc["annotations"] = rawValue(queries)
	} else {
		annotations["list"] = rawValue(queries)
		doc["annotations"] = rawValue(annotations)
	}
	return json.Marshal(doc)
}

func applyDashboardAnnotations(d model.Dashboard, edits []annotationQueryEdit) (model.Dashboard, error) {
	if len(d.Grafana) > 0 && string(d.Grafana) != "null" {
		changed, err := patchSourceAnnotations(d.Grafana, edits)
		if err != nil {
			return d, err
		}
		d.Grafana = changed
	} else {
		var original []json.RawMessage
		if len(d.Annotations) > 0 && json.Unmarshal(d.Annotations, &original) != nil {
			return d, errors.New("invalid stored native annotations")
		}
		queries, err := mergeAnnotationQueries(original, edits)
		if err != nil {
			return d, err
		}
		d.Annotations = rawValue(queries)
	}
	return d, d.Validate()
}

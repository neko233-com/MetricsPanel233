package server

import (
	"github.com/neko233-com/MetricsPanel233/internal/model"
)

var historyLibraryInstanceFields = []string{"id", "gridPos", "repeat", "repeatDirection", "maxPerRow", "scopedVars"}

func historyLibraryUID(object map[string]any) string {
	if ref, ok := object["libraryPanel"].(map[string]any); ok {
		uid, _ := ref["uid"].(string)
		return uid
	}
	return ""
}

// Library models are hydrated caches, not dashboard edits. Compare the shared
// UID and instance placement while keeping other ordinary source fields intact.
func historyLibraryReferences(value any) any {
	switch object := value.(type) {
	case map[string]any:
		if uid := historyLibraryUID(object); uid != "" {
			_, hasType := object["type"]
			_, hasID := object["id"]
			_, hasGrid := object["gridPos"]
			if hasType || hasID || hasGrid {
				instance := map[string]any{"libraryPanel": map[string]any{"uid": uid}}
				for _, key := range historyLibraryInstanceFields {
					if field, ok := object[key]; ok {
						instance[key] = historyLibraryReferences(field)
					}
				}
				return instance
			}
		}
		result := make(map[string]any, len(object))
		for key, field := range object {
			result[key] = historyLibraryReferences(field)
		}
		if object["kind"] == "LibraryPanel" {
			if spec, ok := object["spec"].(map[string]any); ok {
				if uid := historyLibraryUID(spec); uid != "" {
					// V2 retains instance unknown fields in spec; title and the
					// reference's display name come from the shared definition.
					normalized := make(map[string]any, len(spec))
					for key, field := range spec {
						if key != "title" && key != "libraryPanel" {
							normalized[key] = historyLibraryReferences(field)
						}
					}
					normalized["libraryPanel"] = map[string]any{"uid": uid}
					result["spec"] = normalized
				}
			}
		}
		return result
	case []any:
		result := make([]any, len(object))
		for i, field := range object {
			result[i] = historyLibraryReferences(field)
		}
		return result
	default:
		return value
	}
}

func historyRuntime(d model.Dashboard) (any, error) {
	panels := make([]any, 0, len(d.Panels))
	for _, panel := range d.Panels {
		var config any
		if len(panel.Config) > 0 {
			var err error
			config, err = historyJSON(panel.Config)
			if err != nil {
				return nil, err
			}
		}
		if object, ok := config.(map[string]any); ok {
			linked := historyLibraryUID(object) != ""
			if object["kind"] == "LibraryPanel" {
				if spec, ok := object["spec"].(map[string]any); ok {
					linked = historyLibraryUID(spec) != ""
				}
			}
			if linked {
				panels = append(panels, historyLibraryReferences(config))
				continue
			}
		}
		// Native IDs are generated on each classic import. Their only role is
		// local addressing; the preserved config carries the actual panel ID.
		panel.ID = ""
		raw, err := historyJSON(rawValue(panel))
		if err != nil {
			return nil, err
		}
		panels = append(panels, raw)
	}
	variables := []any{}
	if len(d.Variables) > 0 {
		raw, err := historyJSON(rawValue(d.Variables))
		if err != nil {
			return nil, err
		}
		variables = raw.([]any)
	}
	annotations := []any{}
	if len(d.Annotations) > 0 {
		raw, err := historyJSON(d.Annotations)
		if err != nil {
			return nil, err
		}
		if entries, ok := raw.([]any); ok {
			annotations = entries
		}
	}
	return map[string]any{"panels": panels, "variables": variables, "annotations": annotations}, nil
}

// Keep exact source diffs at their existing paths. A native PUT can independently
// change the runtime projection, so compare that durable state as well.
func compareDashboardVersions(base, next model.Dashboard) ([]dashboardChange, error) {
	a, err := historySpec(base)
	if err != nil {
		return nil, err
	}
	b, err := historySpec(next)
	if err != nil {
		return nil, err
	}
	changes := diffDashboardJSON(historyLibraryReferences(a), historyLibraryReferences(b))
	if len(base.Grafana) == 0 || len(next.Grafana) == 0 {
		return changes, nil
	}
	a, err = historyRuntime(base)
	if err != nil {
		return nil, err
	}
	b, err = historyRuntime(next)
	if err != nil {
		return nil, err
	}
	for _, change := range diffDashboardJSON(a, b) {
		change.Path = "/runtime" + change.Path
		changes = append(changes, change)
	}
	return changes, nil
}

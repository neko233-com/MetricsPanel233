// Package grafana imports dashboard schemas without discarding panel contracts.
// The original document is retained, including resource metadata, for export.
package grafana

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/neko233-com/MetricsPanel233/internal/model"
)

type ImportResult struct {
	Dashboard model.Dashboard `json:"dashboard"`
	Warnings  []string        `json:"warnings"`
}

func Import(data []byte) (ImportResult, error) {
	original := append(json.RawMessage(nil), data...)
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return ImportResult{}, err
	}
	if wrapped, ok := root["dashboard"]; ok {
		data = wrapped
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		return ImportResult{}, err
	}
	if spec, ok := doc["spec"]; ok {
		data = spec
	}
	var source struct {
		Title  string            `json:"title"`
		Panels []json.RawMessage `json:"panels"`
		Rows   []struct {
			Panels []json.RawMessage `json:"panels"`
		} `json:"rows"`
		Templating struct {
			List []json.RawMessage `json:"list"`
		} `json:"templating"`
		Elements  map[string]json.RawMessage `json:"elements"`
		Variables []json.RawMessage          `json:"variables"`
		Layout    json.RawMessage            `json:"layout"`
	}
	if err := json.Unmarshal(data, &source); err != nil {
		return ImportResult{}, err
	}
	if source.Title == "" {
		return ImportResult{}, fmt.Errorf("Grafana dashboard title required")
	}
	result := ImportResult{Dashboard: model.Dashboard{ID: uuid.NewString(), Name: source.Title, Panels: []model.Panel{}, Grafana: original}, Warnings: []string{}}
	if len(source.Elements) > 0 {
		panels, warnings, err := normalizeV2(source.Elements, source.Layout)
		if err != nil {
			return result, err
		}
		source.Panels = panels
		result.Warnings = append(result.Warnings, warnings...)
		for _, variable := range source.Variables {
			var v struct {
				Kind string         `json:"kind"`
				Spec map[string]any `json:"spec"`
			}
			if err := json.Unmarshal(variable, &v); err != nil {
				return result, err
			}
			types := map[string]string{"QueryVariable": "query", "CustomVariable": "custom", "ConstantVariable": "constant", "TextVariable": "textbox", "DatasourceVariable": "datasource", "IntervalVariable": "interval", "AdhocVariable": "adhoc"}
			if v.Spec == nil {
				return result, fmt.Errorf("variable %q needs a spec", v.Kind)
			}
			v.Spec["type"] = types[v.Kind]
			if query, ok := v.Spec["query"].(map[string]any); ok {
				v.Spec["query"] = query["spec"]
			}
			b, _ := json.Marshal(v.Spec)
			source.Templating.List = append(source.Templating.List, b)
		}
	}
	var addPanels func([]json.RawMessage) error
	addPanels = func(raws []json.RawMessage) error {
		for _, raw := range raws {
			var p struct {
				Title   string `json:"title"`
				Type    string `json:"type"`
				Targets []struct {
					Expr string `json:"expr"`
					Hide bool   `json:"hide"`
				} `json:"targets"`
				Panels      []json.RawMessage `json:"panels"`
				FieldConfig struct {
					Defaults struct {
						Unit string `json:"unit"`
					} `json:"defaults"`
				} `json:"fieldConfig"`
			}
			if err := json.Unmarshal(raw, &p); err != nil {
				return err
			}
			var snapshot map[string]json.RawMessage
			if err := json.Unmarshal(raw, &snapshot); err != nil {
				return err
			}
			legacySnapshot, hasSnapshot := snapshot["snapshotData"]
			if hasSnapshot && string(legacySnapshot) != "null" {
				frames, err := legacySnapshotFrames(legacySnapshot)
				if err != nil {
					return fmt.Errorf("panel %q: %w", p.Title, err)
				}
				snapshot["datasource"] = json.RawMessage(`{"uid":"grafana","type":"grafana"}`)
				snapshot["targets"], err = json.Marshal([]map[string]any{{"refId": "Snapshot", "queryType": "snapshot", "snapshot": frames}})
				if err != nil {
					return err
				}
				raw, err = json.Marshal(snapshot)
				if err != nil {
					return err
				}
				p.Targets = nil
			}
			if p.Title == "" {
				p.Title = p.Type
				if p.Title == "" {
					p.Title = "Panel"
				}
			}
			if p.Type == "" {
				return fmt.Errorf("panel %q needs a type", p.Title)
			}
			expressions := []string{}
			for _, t := range p.Targets {
				if !t.Hide && t.Expr != "" {
					expressions = append(expressions, t.Expr)
				}
			}
			if len(expressions) > 32 {
				return fmt.Errorf("panel %q has more than 32 targets", p.Title)
			}
			unit := p.FieldConfig.Defaults.Unit
			switch unit {
			case "bytes", "decbytes":
				unit = "bytes"
			case "percent", "percentunit":
				unit = "percent"
			case "s", "dtdurations":
				unit = "seconds"
			case "ops", "reqps":
				unit = "ops"
			default:
				unit = ""
			}
			visual := p.Type
			switch visual {
			case "graph":
				visual = "timeseries"
			case "singlestat":
				visual = "stat"
			}
			switch visual {
			case "timeseries", "stat", "gauge", "bargauge", "table", "text", "row":
			default:
				result.Warnings = append(result.Warnings, fmt.Sprintf("Panel %q: plugin %q requires a compatible renderer; configuration is preserved", p.Title, p.Type))
			}
			panel := model.Panel{ID: uuid.NewString(), Title: p.Title, Aggregation: "last", Unit: unit, Expressions: expressions, Visualization: visual, Config: raw}
			if len(expressions) > 0 {
				panel.Expr = expressions[0]
			}
			result.Dashboard.Panels = append(result.Dashboard.Panels, panel)
			if err := addPanels(p.Panels); err != nil {
				return err
			}
		}
		return nil
	}
	if err := addPanels(source.Panels); err != nil {
		return result, err
	}
	for _, row := range source.Rows {
		if err := addPanels(row.Panels); err != nil {
			return result, err
		}
	}
	for _, raw := range source.Templating.List {
		var v struct {
			Name    string          `json:"name"`
			Type    string          `json:"type"`
			Query   json.RawMessage `json:"query"`
			Current struct {
				Value json.RawMessage `json:"value"`
			} `json:"current"`
			Options []struct {
				Value json.RawMessage `json:"value"`
			} `json:"options"`
			Multi      bool `json:"multi"`
			IncludeAll bool `json:"includeAll"`
		}
		if err := json.Unmarshal(raw, &v); err != nil {
			return result, err
		}
		query := stringValue(v.Query)
		if query == "" {
			var obj struct {
				Query string `json:"query"`
			}
			_ = json.Unmarshal(v.Query, &obj)
			query = obj.Query
		}
		current := stringValue(v.Current.Value)
		options := []string{}
		for _, o := range v.Options {
			if value := stringValue(o.Value); value != "" && value != "$__all" {
				options = append(options, value)
			}
		}
		if (v.Type == "custom" || v.Type == "interval") && len(options) == 0 {
			for _, x := range strings.Split(query, ",") {
				options = append(options, strings.TrimSpace(x))
			}
		}
		if v.Type == "constant" {
			current = query
		}
		result.Dashboard.Variables = append(result.Dashboard.Variables, model.Variable{Name: v.Name, Type: v.Type, Query: query, Current: current, Options: options, Multi: v.Multi, IncludeAll: v.IncludeAll, Config: raw})
	}
	if len(result.Dashboard.Panels) == 0 {
		return result, fmt.Errorf("template has no panels")
	}
	if err := result.Dashboard.Validate(); err != nil {
		return result, err
	}
	return result, nil
}

func stringValue(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var values []string
	if json.Unmarshal(raw, &values) == nil {
		return strings.Join(values, "|")
	}
	return ""
}

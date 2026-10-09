// Package grafana imports Prometheus-backed Grafana classic dashboard JSON.
// Original JSON is preserved for lossless export; incompatible features are
// reported explicitly instead of silently presenting an inaccurate panel.
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
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return ImportResult{}, err
	}
	if wrapped, ok := root["dashboard"]; ok {
		data = wrapped
	}
	type target struct {
		Expr       string          `json:"expr"`
		Hide       bool            `json:"hide"`
		Datasource json.RawMessage `json:"datasource"`
	}
	type rawPanel struct {
		ID              int               `json:"id"`
		Title           string            `json:"title"`
		Type            string            `json:"type"`
		Targets         []target          `json:"targets"`
		Panels          []json.RawMessage `json:"panels"`
		Transformations []json.RawMessage `json:"transformations"`
		Repeat          string            `json:"repeat"`
		FieldConfig     struct {
			Defaults struct {
				Unit string `json:"unit"`
			} `json:"defaults"`
		} `json:"fieldConfig"`
		YAxes []struct {
			Format string `json:"format"`
		} `json:"yaxes"`
	}
	var source struct {
		Title  string            `json:"title"`
		Panels []json.RawMessage `json:"panels"`
		Rows   []struct {
			Panels []json.RawMessage `json:"panels"`
		} `json:"rows"`
		Templating struct {
			List []struct {
				Name    string          `json:"name"`
				Type    string          `json:"type"`
				Query   json.RawMessage `json:"query"`
				Current struct {
					Value json.RawMessage `json:"value"`
				} `json:"current"`
				Options []struct {
					Value string `json:"value"`
				} `json:"options"`
				Multi      bool `json:"multi"`
				IncludeAll bool `json:"includeAll"`
			} `json:"list"`
		} `json:"templating"`
	}
	if err := json.Unmarshal(data, &source); err != nil {
		return ImportResult{}, err
	}
	if source.Title == "" {
		return ImportResult{}, fmt.Errorf("Grafana dashboard title required")
	}
	result := ImportResult{Dashboard: model.Dashboard{ID: uuid.NewString(), Name: source.Title, Panels: []model.Panel{}, Grafana: json.RawMessage(data)}, Warnings: []string{}}
	var addPanels func([]json.RawMessage) error
	addPanels = func(raws []json.RawMessage) error {
		for _, raw := range raws {
			var p rawPanel
			if err := json.Unmarshal(raw, &p); err != nil {
				return err
			}
			if p.Type == "row" {
				if err := addPanels(p.Panels); err != nil {
					return err
				}
				continue
			}
			switch p.Type {
			case "timeseries", "graph", "stat", "singlestat", "gauge", "bargauge", "table":
			default:
				result.Warnings = append(result.Warnings, fmt.Sprintf("Panel %q uses unsupported type %q and was skipped", p.Title, p.Type))
				continue
			}
			if len(p.Transformations) > 0 || p.Repeat != "" {
				result.Warnings = append(result.Warnings, fmt.Sprintf("Panel %q uses transformations or repeat; these are not applied", p.Title))
			}
			expressions := []string{}
			for _, t := range p.Targets {
				if !t.Hide && t.Expr != "" {
					expressions = append(expressions, t.Expr)
				}
			}
			if len(expressions) == 0 {
				result.Warnings = append(result.Warnings, fmt.Sprintf("Panel %q has no PromQL targets and was skipped", p.Title))
				continue
			}
			if len(expressions) > 8 {
				return fmt.Errorf("panel %q has more than 8 targets", p.Title)
			}
			unit := p.FieldConfig.Defaults.Unit
			if unit == "" && len(p.YAxes) > 0 {
				unit = p.YAxes[0].Format
			}
			if unit == "percentunit" {
				for i, expr := range expressions {
					expressions[i] = "(" + expr + ") * 100"
				}
			}
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
			if visual == "gauge" || visual == "bargauge" {
				result.Warnings = append(result.Warnings, fmt.Sprintf("Panel %q: %s is displayed as a numeric stat", p.Title, p.Type))
			}
			if visual == "graph" {
				visual = "timeseries"
			}
			if visual == "singlestat" || visual == "gauge" || visual == "bargauge" {
				visual = "stat"
			}
			result.Dashboard.Panels = append(result.Dashboard.Panels, model.Panel{ID: uuid.NewString(), Title: p.Title, Aggregation: "last", Unit: unit, Expr: expressions[0], Expressions: expressions, Visualization: visual})
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
	for _, v := range source.Templating.List {
		if v.Type == "datasource" {
			continue
		}
		switch v.Type {
		case "query", "custom", "constant", "textbox", "interval":
		default:
			result.Warnings = append(result.Warnings, fmt.Sprintf("Variable %q has unsupported type %q", v.Name, v.Type))
			continue
		}
		var query string
		if err := json.Unmarshal(v.Query, &query); err != nil {
			var obj struct {
				Query string `json:"query"`
			}
			_ = json.Unmarshal(v.Query, &obj)
			query = obj.Query
		}
		var current string
		if err := json.Unmarshal(v.Current.Value, &current); err != nil {
			var values []string
			if json.Unmarshal(v.Current.Value, &values) == nil {
				current = strings.Join(values, "|")
			}
		}
		options := []string{}
		for _, o := range v.Options {
			if o.Value != "$__all" {
				options = append(options, o.Value)
			}
		}
		if v.Type == "custom" || v.Type == "interval" {
			options = strings.Split(query, ",")
		}
		if v.Type == "constant" {
			current = query
		}
		if v.IncludeAll && current == "$__all" {
			current = ".*"
		}
		result.Dashboard.Variables = append(result.Dashboard.Variables, model.Variable{Name: v.Name, Type: v.Type, Query: query, Current: current, Options: options, Multi: v.Multi, IncludeAll: v.IncludeAll})
	}
	if len(result.Dashboard.Panels) == 0 {
		return result, fmt.Errorf("template has no supported Prometheus panels")
	}
	if err := result.Dashboard.Validate(); err != nil {
		return result, err
	}
	return result, nil
}

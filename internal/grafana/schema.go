package grafana

import (
	"encoding/json"
	"fmt"
	"sort"
)

// normalizeV2 translates public v2 element/query contracts to classic panels.
// The original resource is retained independently for lossless export.
func normalizeV2(elements map[string]json.RawMessage, layout json.RawMessage) ([]json.RawMessage, []string, error) {
	warnings := []string{}
	panels := map[string]map[string]any{}
	for name, raw := range elements {
		var element struct {
			Kind string         `json:"kind"`
			Spec map[string]any `json:"spec"`
		}
		if err := json.Unmarshal(raw, &element); err != nil {
			return nil, nil, err
		}
		p := element.Spec
		if element.Kind != "Panel" {
			warnings = append(warnings, fmt.Sprintf("Element %q (%s) needs a library resolver", name, element.Kind))
			continue
		}
		if p == nil {
			return nil, nil, fmt.Errorf("element %q needs a spec", name)
		}
		viz := object(p["vizConfig"])
		spec := object(viz["spec"])
		p["type"] = viz["group"]
		p["options"] = spec["options"]
		p["fieldConfig"] = spec["fieldConfig"]
		data := object(object(p["data"])["spec"])
		targets := []any{}
		for _, rawQuery := range array(data["queries"]) {
			q := object(object(rawQuery)["spec"])
			query := object(q["query"])
			target := object(query["spec"])
			target["refId"] = q["refId"]
			target["hide"] = q["hidden"]
			target["datasource"] = map[string]any{"type": query["group"], "uid": object(query["datasource"])["name"]}
			targets = append(targets, target)
		}
		p["targets"] = targets
		transforms := []any{}
		for _, rawTransform := range array(data["transformations"]) {
			t := object(rawTransform)
			spec := object(t["spec"])
			if spec["id"] == nil {
				spec["id"] = t["kind"]
			}
			transforms = append(transforms, spec)
		}
		p["transformations"] = transforms
		for key, value := range object(data["queryOptions"]) {
			p[key] = value
		}
		panels[name] = p
	}
	ordered := []string{}
	seen := map[string]bool{}
	rowY := 0
	var walk func(map[string]any)
	walk = func(l map[string]any) {
		kind, _ := l["kind"].(string)
		spec := object(l["spec"])
		switch kind {
		case "GridLayout", "AutoGridLayout":
			columns := 2
			if n, ok := spec["maxColumnCount"].(float64); ok && n >= 1 && n <= 24 {
				columns = int(n)
			}
			items := array(spec["items"])
			bottom := rowY
			for i, rawItem := range items {
				item := object(object(rawItem)["spec"])
				name, _ := object(item["element"])["name"].(string)
				p := panels[name]
				if p == nil || seen[name] {
					continue
				}
				pos := map[string]any{"x": (i % columns) * (24 / columns), "y": rowY + (i/columns)*8, "w": 24 / columns, "h": 8}
				if kind == "GridLayout" {
					pos = map[string]any{"x": item["x"], "y": number(item["y"]) + float64(rowY), "w": item["width"], "h": item["height"]}
				}
				p["gridPos"] = pos
				if repeat := object(item["repeat"]); repeat["value"] != nil {
					p["repeat"] = repeat["value"]
					p["repeatDirection"] = repeat["direction"]
					p["maxPerRow"] = repeat["maxPerRow"]
				}
				if item["conditionalRendering"] != nil {
					warnings = append(warnings, "Conditional layout visibility is preserved in the source but is not evaluated")
				}
				end := int(number(pos["y"]) + number(pos["h"]))
				if end > bottom {
					bottom = end
				}
				ordered = append(ordered, name)
				seen[name] = true
			}
			rowY = bottom
		case "RowsLayout":
			for _, row := range array(spec["rows"]) {
				r := object(object(row)["spec"])
				walk(object(r["layout"]))
			}
		case "TabsLayout":
			warnings = append(warnings, "Tab layouts currently display each tab in document order")
			for _, tab := range array(spec["tabs"]) {
				walk(object(object(object(tab)["spec"])["layout"]))
			}
		}
	}
	var l map[string]any
	if len(layout) > 0 {
		if err := json.Unmarshal(layout, &l); err != nil {
			return nil, nil, err
		}
		walk(l)
	}
	rest := []string{}
	for name := range panels {
		if !seen[name] {
			rest = append(rest, name)
		}
	}
	sort.Strings(rest)
	ordered = append(ordered, rest...)
	result := []json.RawMessage{}
	for _, name := range ordered {
		b, err := json.Marshal(panels[name])
		if err != nil {
			return nil, nil, err
		}
		result = append(result, b)
	}
	return result, warnings, nil
}
func object(value any) map[string]any {
	if o, ok := value.(map[string]any); ok && o != nil {
		return o
	}
	return map[string]any{}
}
func array(value any) []any {
	if a, ok := value.([]any); ok {
		return a
	}
	return nil
}
func number(value any) float64 {
	if n, ok := value.(float64); ok {
		return n
	}
	if n, ok := value.(int); ok {
		return float64(n)
	}
	return 0
}

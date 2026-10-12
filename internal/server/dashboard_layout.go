package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/neko233-com/MetricsPanel233/internal/model"
)

type panelPosition struct {
	ID string `json:"id"`
	X  int    `json:"x"`
	Y  int    `json:"y"`
	W  int    `json:"w"`
	H  int    `json:"h"`
}

func (p panelPosition) grid() rawObject {
	return rawObject{"x": rawValue(p.X), "y": rawValue(p.Y), "w": rawValue(p.W), "h": rawValue(p.H)}
}

func (s *Server) dashboardLayoutRoutes(api *http.ServeMux) {
	api.HandleFunc("PATCH /api/v1/dashboards/{id}/layout", s.saveDashboardLayout)
}

func (s *Server) saveDashboardLayout(w http.ResponseWriter, r *http.Request) {
	text := strings.TrimSpace(r.Header.Get("If-Match"))
	if text == "" {
		fail(w, http.StatusPreconditionRequired, errors.New("If-Match dashboard revision is required"))
		return
	}
	if len(text) >= 2 && text[0] == '"' && text[len(text)-1] == '"' {
		text = text[1 : len(text)-1]
	}
	revision, err := strconv.ParseInt(text, 10, 64)
	if err != nil || revision <= 0 {
		fail(w, 400, errors.New("If-Match must contain a positive dashboard revision"))
		return
	}
	ctx, err := dashboardSaveContext(r)
	if err != nil {
		fail(w, 400, err)
		return
	}
	var input struct {
		Panels []panelPosition `json:"panels"`
	}
	if err = decode(w, r, &input); err != nil {
		fail(w, 400, err)
		return
	}
	d, err := s.Store.Dashboard(ctx, r.PathValue("id"))
	if err != nil {
		dashboardResourceError(w, err)
		return
	}
	if d.UpdatedAt != revision {
		fail(w, 409, errors.New("dashboard changed; reload before saving its layout"))
		return
	}
	d, err = applyDashboardLayout(d, input.Panels)
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
}

// Only the position is accepted from the browser. Query models, plugin options,
// library definitions and wide numeric literals are read from the stored source.
func applyDashboardLayout(d model.Dashboard, positions []panelPosition) (model.Dashboard, error) {
	if len(positions) != len(d.Panels) || len(positions) > 500 {
		return d, errors.New("layout must contain every dashboard panel exactly once")
	}
	byID := make(map[string]panelPosition, len(positions))
	for _, p := range positions {
		if p.ID == "" || p.X < 0 || p.X > 23 || p.Y < 0 || p.Y > 10000 || p.W < 1 || p.W > 24-p.X || p.H < 1 || p.H > 100 {
			return d, errors.New("panel position requires x=0–23, y=0–10000, width within 24 columns and height=1–100")
		}
		if _, exists := byID[p.ID]; exists {
			return d, errors.New("duplicate panel ID in layout")
		}
		byID[p.ID] = p
	}
	// Copy the slice because failed source validation must not mutate the caller.
	d.Panels = append([]model.Panel(nil), d.Panels...)
	sourcePositions := make(map[string]panelPosition, len(d.Panels))
	for i := range d.Panels {
		panel := &d.Panels[i]
		p, exists := byID[panel.ID]
		if !exists {
			return d, fmt.Errorf("missing dashboard panel %q", panel.ID)
		}
		config := rawObject{}
		if len(panel.Config) > 0 && json.Unmarshal(panel.Config, &config) != nil {
			return d, errors.New("invalid saved panel configuration")
		}
		if config == nil {
			config = rawObject{}
		}
		if id := string(config["id"]); id != "" {
			if _, duplicate := sourcePositions[id]; duplicate && len(d.Grafana) > 0 {
				return d, errors.New("imported dashboard has ambiguous panel IDs")
			}
			sourcePositions[id] = p
		}
		config["gridPos"] = rawValue(p.grid())
		panel.Config = rawValue(config)
	}
	if len(d.Grafana) == 0 || string(d.Grafana) == "null" {
		return d, nil
	}
	raw, err := patchDashboardDocumentLayout(d.Grafana, sourcePositions, d.Panels, byID)
	if err != nil {
		return d, err
	}
	d.Grafana = raw
	return d, nil
}

func patchDashboardDocumentLayout(raw json.RawMessage, positions map[string]panelPosition, panels []model.Panel, byID map[string]panelPosition) (json.RawMessage, error) {
	var doc rawObject
	if err := json.Unmarshal(raw, &doc); err != nil || doc == nil {
		return nil, errors.New("invalid saved Grafana dashboard")
	}
	for _, wrapper := range []string{"dashboard", "spec"} {
		if value, exists := doc[wrapper]; exists {
			next, err := patchDashboardDocumentLayout(value, positions, panels, byID)
			if err != nil {
				return nil, err
			}
			doc[wrapper] = next
			return json.Marshal(doc)
		}
	}
	if _, v2 := doc["elements"]; v2 {
		var elements map[string]json.RawMessage
		if err := json.Unmarshal(doc["elements"], &elements); err != nil {
			return nil, err
		}
		byName := map[string]panelPosition{}
		for name, element := range elements {
			var envelope struct {
				Spec rawObject `json:"spec"`
			}
			if json.Unmarshal(element, &envelope) != nil {
				return nil, errors.New("invalid V2 element")
			}
			if p, exists := positions[string(envelope.Spec["id"])]; exists {
				byName[name] = p
			}
		}
		seen := map[string]bool{}
		layout, _, err := patchV2Layout(doc["layout"], byName, seen, 0)
		if err != nil {
			return nil, err
		}
		if len(seen) != len(panels) {
			return nil, errors.New("every V2 panel must have a unique element ID and layout reference")
		}
		doc["layout"] = layout
		return json.Marshal(doc)
	}
	index := 0
	var walk func(json.RawMessage) (json.RawMessage, error)
	walk = func(value json.RawMessage) (json.RawMessage, error) {
		var list []rawObject
		if err := json.Unmarshal(value, &list); err != nil {
			return nil, err
		}
		for _, source := range list {
			if index >= len(panels) {
				return nil, errors.New("Grafana source has extra panels")
			}
			p := byID[panels[index].ID]
			var config rawObject
			_ = json.Unmarshal(panels[index].Config, &config)
			if string(source["id"]) != string(config["id"]) {
				return nil, errors.New("Grafana source panel order differs from saved dashboard")
			}
			index++
			source["gridPos"] = rawValue(p.grid())
			if children, exists := source["panels"]; exists {
				next, err := walk(children)
				if err != nil {
					return nil, err
				}
				source["panels"] = next
			}
		}
		return json.Marshal(list)
	}
	if value, exists := doc["panels"]; exists {
		next, err := walk(value)
		if err != nil {
			return nil, err
		}
		doc["panels"] = next
	}
	if value, exists := doc["rows"]; exists {
		var rows []rawObject
		if err := json.Unmarshal(value, &rows); err != nil {
			return nil, err
		}
		for _, row := range rows {
			if value, exists := row["panels"]; exists {
				next, err := walk(value)
				if err != nil {
					return nil, err
				}
				row["panels"] = next
			}
		}
		doc["rows"] = rawValue(rows)
	}
	if index != len(panels) {
		return nil, errors.New("Grafana source is missing saved panels")
	}
	return json.Marshal(doc)
}

// Rows and tabs retain their containers. Their child coordinates are relative to
// the preceding section's bottom, matching the importer. Cross-section moves
// cannot be represented by a position-only edit and are rejected explicitly.
func patchV2Layout(raw json.RawMessage, positions map[string]panelPosition, seen map[string]bool, baseY int) (json.RawMessage, int, error) {
	var layout rawObject
	if json.Unmarshal(raw, &layout) != nil || layout == nil {
		return nil, 0, errors.New("V2 dashboard requires a layout")
	}
	var spec rawObject
	if json.Unmarshal(layout["spec"], &spec) != nil || spec == nil {
		return nil, 0, errors.New("invalid V2 layout spec")
	}
	bottom := baseY
	switch rawString(layout["kind"]) {
	case "GridLayout", "AutoGridLayout":
		var items []rawObject
		if err := json.Unmarshal(spec["items"], &items); err != nil {
			return nil, 0, err
		}
		for _, item := range items {
			var content rawObject
			if json.Unmarshal(item["spec"], &content) != nil {
				return nil, 0, errors.New("invalid V2 layout item")
			}
			var reference struct {
				Name string `json:"name"`
			}
			_ = json.Unmarshal(content["element"], &reference)
			p, exists := positions[reference.Name]
			if !exists || seen[reference.Name] {
				return nil, 0, errors.New("V2 layout has a missing or duplicated panel reference")
			}
			if p.Y < baseY {
				return nil, 0, errors.New("a panel cannot move above its V2 row or tab; keep section order")
			}
			seen[reference.Name] = true
			content["x"], content["y"] = rawValue(p.X), rawValue(p.Y-baseY)
			content["width"], content["height"] = rawValue(p.W), rawValue(p.H)
			item["kind"] = rawValue("GridLayoutItem")
			item["spec"] = rawValue(content)
			bottom = max(bottom, p.Y+p.H)
		}
		layout["kind"] = rawValue("GridLayout")
		spec["items"] = rawValue(items)
	case "RowsLayout", "TabsLayout":
		key := "rows"
		if rawString(layout["kind"]) == "TabsLayout" {
			key = "tabs"
		}
		var sections []rawObject
		if err := json.Unmarshal(spec[key], &sections); err != nil {
			return nil, 0, err
		}
		for _, section := range sections {
			var content rawObject
			if json.Unmarshal(section["spec"], &content) != nil {
				return nil, 0, errors.New("invalid V2 section")
			}
			next, end, err := patchV2Layout(content["layout"], positions, seen, bottom)
			if err != nil {
				return nil, 0, err
			}
			content["layout"] = next
			section["spec"] = rawValue(content)
			bottom = end
		}
		spec[key] = rawValue(sections)
	default:
		return nil, 0, errors.New("unsupported V2 layout kind")
	}
	layout["spec"] = rawValue(spec)
	next, err := json.Marshal(layout)
	return next, bottom, err
}

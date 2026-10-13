package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"github.com/neko233-com/MetricsPanel233/internal/grafana"
	"github.com/neko233-com/MetricsPanel233/internal/model"
)

type rawObject = map[string]json.RawMessage

func rawString(raw json.RawMessage) string { var s string; _ = json.Unmarshal(raw, &s); return s }
func rawValue(v any) json.RawMessage       { b, _ := json.Marshal(v); return b }

// transformRaw retains numeric literals and untouched plugin fields. It only
// serializes containers whose child or panel was changed.
func transformRaw(raw json.RawMessage, visit func(rawObject) (rawObject, bool, error)) (json.RawMessage, error) {
	var object rawObject
	if json.Unmarshal(raw, &object) == nil && object != nil {
		object, changed, err := visit(object)
		if err != nil {
			return nil, err
		}
		for key, child := range object {
			if key == "__elements" || key == "_metricspanelLibrarySource" {
				continue
			}
			next, err := transformRaw(child, visit)
			if err != nil {
				return nil, err
			}
			if string(next) != string(child) {
				changed = true
				object[key] = next
			}
		}
		if !changed {
			return raw, nil
		}
		return json.Marshal(object)
	}
	var array []json.RawMessage
	if json.Unmarshal(raw, &array) == nil && array != nil {
		changed := false
		for i, child := range array {
			next, err := transformRaw(child, visit)
			if err != nil {
				return nil, err
			}
			if string(next) != string(child) {
				array[i] = next
				changed = true
			}
		}
		if changed {
			return json.Marshal(array)
		}
	}
	return raw, nil
}
func libraryDefinitions(raw json.RawMessage, folderUID string) ([]model.LibraryElement, error) {
	out := []model.LibraryElement{}
	var walk func(json.RawMessage) error
	walk = func(value json.RawMessage) error {
		var object rawObject
		if json.Unmarshal(value, &object) != nil || object == nil {
			return nil
		}
		if entries := object["__elements"]; len(entries) > 0 {
			var defs map[string]json.RawMessage
			if err := json.Unmarshal(entries, &defs); err != nil {
				var list []json.RawMessage
				if json.Unmarshal(entries, &list) != nil {
					return errors.New("__elements must be a library panel array or map")
				}
				defs = map[string]json.RawMessage{}
				for _, entry := range list {
					var identity struct {
						UID string `json:"uid"`
					}
					if json.Unmarshal(entry, &identity) != nil || identity.UID == "" {
						return errors.New("every __elements library panel needs a UID")
					}
					if _, exists := defs[identity.UID]; exists {
						return errors.New("duplicate library panel UID in __elements")
					}
					defs[identity.UID] = entry
				}
			}
			for key, entry := range defs {
				var e model.LibraryElement
				if err := json.Unmarshal(entry, &e); err != nil {
					return err
				}
				if e.UID == "" {
					e.UID = key
				}
				if e.Kind == 0 {
					e.Kind = 1
				}
				e.FolderUID = folderUID
				if e.Name == "" {
					var panel rawObject
					_ = json.Unmarshal(e.Model, &panel)
					e.Name = rawString(panel["title"])
				}
				if err := e.Validate(); err != nil {
					return fmt.Errorf("library element %s: %w", key, err)
				}
				out = append(out, e)
			}
		}
		for _, key := range []string{"dashboard", "spec"} {
			if child := object[key]; len(child) > 0 {
				if err := walk(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	err := walk(raw)
	return out, err
}
func classicLibraryV2(panel rawObject, instance rawObject) rawObject {
	viz := rawObject{"kind": rawValue("VizConfig"), "group": panel["type"], "version": rawValue(""), "spec": rawValue(rawObject{"options": panel["options"], "fieldConfig": panel["fieldConfig"]})}
	queries := []rawObject{}
	var targets []rawObject
	_ = json.Unmarshal(panel["targets"], &targets)
	for _, target := range targets {
		query := rawObject{}
		for key, value := range target {
			if key != "datasource" && key != "refId" && key != "hide" {
				query[key] = value
			}
		}
		var ds rawObject
		_ = json.Unmarshal(target["datasource"], &ds)
		if ds == nil {
			_ = json.Unmarshal(panel["datasource"], &ds)
		}
		group := rawString(ds["type"])
		uid := rawString(ds["uid"])
		if uid == "" {
			uid = rawString(target["datasource"])
			if uid == "" {
				uid = rawString(panel["datasource"])
			}
		}
		if group == "" {
			group = "prometheus"
		}
		queries = append(queries, rawObject{"kind": rawValue("PanelQuery"), "spec": rawValue(rawObject{"refId": target["refId"], "hidden": target["hide"], "query": rawValue(rawObject{"kind": rawValue("DataQuery"), "group": rawValue(group), "datasource": rawValue(map[string]string{"name": uid}), "spec": rawValue(query)})})})
	}
	transforms := []rawObject{}
	var sourceTransforms []rawObject
	_ = json.Unmarshal(panel["transformations"], &sourceTransforms)
	for _, t := range sourceTransforms {
		transforms = append(transforms, rawObject{"kind": t["id"], "spec": rawValue(t)})
	}
	options := rawObject{}
	for _, key := range []string{"maxDataPoints", "interval", "timeFrom", "timeShift", "hideTimeOverride", "cacheTimeout", "queryCachingTTL"} {
		if value := panel[key]; len(value) > 0 {
			options[key] = value
		}
	}
	spec := rawObject{"id": instance["id"], "title": panel["title"], "description": panel["description"], "links": panel["links"], "libraryPanel": instance["libraryPanel"], "vizConfig": rawValue(viz), "data": rawValue(rawObject{"kind": rawValue("QueryGroup"), "spec": rawValue(rawObject{"queries": rawValue(queries), "transformations": rawValue(transforms), "queryOptions": rawValue(options)})})}
	for key, value := range panel {
		switch key {
		case "id", "type", "title", "description", "links", "libraryPanel", "gridPos", "options", "fieldConfig", "targets", "datasource", "transformations", "maxDataPoints", "interval", "timeFrom", "timeShift", "hideTimeOverride", "cacheTimeout", "queryCachingTTL":
		default:
			spec[key] = value
		}
	}
	for key, value := range spec {
		if len(value) == 0 {
			delete(spec, key)
		}
	}
	return rawObject{"kind": rawValue("Panel"), "spec": rawValue(spec)}
}
func (s *Server) resolveGrafanaLibraryPanels(ctx context.Context, raw []byte, folderUID string) ([]byte, error) {
	defs, err := libraryDefinitions(raw, folderUID)
	if err != nil {
		return nil, err
	}
	pending := map[string]model.LibraryElement{}
	for _, e := range defs {
		pending[e.UID] = e
	}
	cache := map[string]model.LibraryElement{}
	expansions := 0
	lookup := func(uid string) (model.LibraryElement, error) {
		expansions++
		if expansions > 1024 {
			return model.LibraryElement{}, errors.New("library panel expansion exceeds 1024 references or contains a cycle")
		}
		if e, ok := cache[uid]; ok {
			return e, nil
		}
		if err := s.authorizeLibraryRead(ctx, uid); err != nil {
			return model.LibraryElement{}, err
		}
		e, err := s.Store.LibraryElement(ctx, uid)
		if errors.Is(err, sql.ErrNoRows) {
			var ok bool
			e, ok = pending[uid]
			if ok {
				err = nil
			}
		}
		if err != nil {
			return e, fmt.Errorf("library panel %s: %w", uid, err)
		}
		cache[uid] = e
		return e, nil
	}
	return transformRaw(raw, func(object rawObject) (rawObject, bool, error) {
		if rawString(object["kind"]) == "LibraryPanel" {
			var instance rawObject
			if err := json.Unmarshal(object["spec"], &instance); err != nil {
				return nil, false, err
			}
			var ref struct {
				UID string `json:"uid"`
			}
			_ = json.Unmarshal(instance["libraryPanel"], &ref)
			e, err := lookup(ref.UID)
			if err != nil {
				return nil, false, err
			}
			var panel rawObject
			_ = json.Unmarshal(e.Model, &panel)
			resolved := classicLibraryV2(panel, instance)
			resolved["_metricspanelLibrarySource"] = rawValue(object)
			return resolved, true, nil
		}
		link := object["libraryPanel"]
		if len(link) == 0 || (len(object["type"]) == 0 && len(object["id"]) == 0 && len(object["gridPos"]) == 0) || (len(object["vizConfig"]) > 0 && len(object["type"]) == 0) {
			return object, false, nil
		}
		var ref struct {
			UID string `json:"uid"`
		}
		_ = json.Unmarshal(link, &ref)
		if ref.UID == "" {
			return object, false, nil
		}
		e, err := lookup(ref.UID)
		if err != nil {
			return nil, false, err
		}
		var panel rawObject
		_ = json.Unmarshal(e.Model, &panel)
		for _, key := range []string{"id", "gridPos", "repeat", "repeatDirection", "maxPerRow", "scopedVars"} {
			if value, ok := object[key]; ok {
				panel[key] = value
			}
		}
		panel["libraryPanel"] = rawValue(map[string]any{"uid": e.UID, "name": e.Name})
		return panel, true, nil
	})
}
func (s *Server) saveDashboardWithLibraryPanels(ctx context.Context, d model.Dashboard, revision *int64) (model.Dashboard, error) {
	defs, err := libraryDefinitions(d.Grafana, d.FolderUID)
	if err != nil {
		return d, err
	}
	// Retain imported source metadata used for future export rebinding. Library
	// definitions live in their own table; resource schemas retain only references.
	d.Grafana, err = transformRaw(d.Grafana, func(object rawObject) (rawObject, bool, error) {
		changed := false
		if _, ok := object["__elements"]; ok {
			delete(object, "__elements")
			changed = true
		}
		if rawString(object["kind"]) == "Panel" {
			if original := object["_metricspanelLibrarySource"]; len(original) > 0 {
				var saved rawObject
				if err := json.Unmarshal(original, &saved); err != nil {
					return nil, false, err
				}
				return saved, true, nil
			}
			var spec rawObject
			_ = json.Unmarshal(object["spec"], &spec)
			if len(spec["libraryPanel"]) > 0 {
				object = rawObject{"kind": rawValue("LibraryPanel"), "spec": rawValue(rawObject{"id": spec["id"], "title": spec["title"], "libraryPanel": spec["libraryPanel"]})}
				changed = true
			}
		}
		return object, changed, nil
	})
	if err != nil {
		return d, err
	}
	return s.Store.SaveDashboardWithLibraryElements(ctx, d, defs, revision)
}
func (s *Server) resolveDashboardLibrary(ctx context.Context, d model.Dashboard) (model.Dashboard, error) {
	for i, p := range d.Panels {
		if dashboardJSONObjects(p.Config) == nil {
			continue
		}
		resolved, err := s.resolveGrafanaLibraryPanels(ctx, p.Config, d.FolderUID)
		if err != nil {
			return d, err
		}
		if string(resolved) == string(p.Config) {
			continue
		}
		result, err := grafana.Import(rawValue(rawObject{"title": rawValue(d.Name), "panels": rawValue([]json.RawMessage{resolved})}))
		if err != nil {
			return d, err
		}
		if len(result.Dashboard.Panels) > 0 {
			next := result.Dashboard.Panels[0]
			next.ID = p.ID
			d.Panels[i] = next
		}
	}
	return d, nil
}
func (s *Server) moveDashboardFolder(w http.ResponseWriter, r *http.Request) {
	var cmd struct {
		FolderUID string `json:"folderUid"`
		Revision  *int64 `json:"revision"`
	}
	if err := decode(w, r, &cmd); err != nil {
		fail(w, 400, err)
		return
	}
	d, err := s.Store.Dashboard(r.Context(), r.PathValue("id"))
	if err != nil {
		dashboardResourceError(w, err)
		return
	}
	if cmd.Revision == nil {
		cmd.Revision = &d.UpdatedAt
	}
	f, err := s.resolveDashboardFolder(r.Context(), cmd.FolderUID, 0)
	if err != nil {
		dashboardResourceError(w, err)
		return
	}
	d.FolderUID = f.UID
	d, err = s.saveDashboardWithLibraryPanels(r.Context(), d, cmd.Revision)
	if err != nil {
		dashboardResourceError(w, err)
		return
	}
	writeJSON(w, 200, d)
}
func (s *Server) insertDashboardLibrary(w http.ResponseWriter, r *http.Request) {
	var cmd struct {
		LibraryUID string `json:"libraryUid"`
		Revision   *int64 `json:"revision"`
	}
	if err := decode(w, r, &cmd); err != nil {
		fail(w, 400, err)
		return
	}
	d, err := s.Store.Dashboard(r.Context(), r.PathValue("id"))
	if err != nil {
		dashboardResourceError(w, err)
		return
	}
	if cmd.Revision == nil {
		cmd.Revision = &d.UpdatedAt
	}
	e, err := s.Store.LibraryElement(r.Context(), cmd.LibraryUID)
	if err != nil {
		dashboardResourceError(w, err)
		return
	}
	var panel rawObject
	_ = json.Unmarshal(e.Model, &panel)
	nextID := int64(1)
	bottom := int64(0)
	for i, p := range d.Panels {
		var old struct {
			ID   int64 `json:"id"`
			Grid struct {
				Y int64 `json:"y"`
				H int64 `json:"h"`
			} `json:"gridPos"`
		}
		if dashboardJSONObjects(p.Config) == nil {
			old.ID = int64(i + 1)
			old.Grid.Y = int64(i / 2 * 8)
			old.Grid.H = 8
		}
		_ = json.Unmarshal(p.Config, &old)
		if old.ID >= nextID {
			nextID = old.ID + 1
		}
		if old.Grid.Y+old.Grid.H > bottom {
			bottom = old.Grid.Y + old.Grid.H
		}
	}
	panel["id"] = rawValue(nextID)
	panel["gridPos"] = rawValue(map[string]int64{"x": 0, "y": bottom, "w": 12, "h": 8})
	panel["libraryPanel"] = rawValue(map[string]string{"uid": e.UID, "name": e.Name})
	raw := rawValue(panel)
	result, err := grafana.Import(rawValue(rawObject{"title": rawValue(d.Name), "panels": rawValue([]json.RawMessage{raw})}))
	if err != nil {
		dashboardResourceError(w, err)
		return
	}
	p := result.Dashboard.Panels[0]
	p.ID = uuid.NewString()
	d.Panels = append(d.Panels, p)
	if len(d.Grafana) > 0 && string(d.Grafana) != "null" {
		d.Grafana, err = appendLibrarySource(d.Grafana, raw, nextID)
		if err != nil {
			dashboardResourceError(w, err)
			return
		}
	}
	d, err = s.saveDashboardWithLibraryPanels(r.Context(), d, cmd.Revision)
	if err != nil {
		dashboardResourceError(w, err)
		return
	}
	writeJSON(w, 200, d)
}
func appendLibrarySource(raw json.RawMessage, panel json.RawMessage, id int64) (json.RawMessage, error) {
	var object rawObject
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, err
	}
	if object == nil {
		return nil, errors.New("dashboard source must be a JSON object")
	}
	for _, key := range []string{"dashboard", "spec"} {
		if child, ok := object[key]; ok {
			next, err := appendLibrarySource(child, panel, id)
			if err != nil {
				return nil, err
			}
			object[key] = next
			return json.Marshal(object)
		}
	}
	if elementsRaw, ok := object["elements"]; ok {
		var elements rawObject
		if err := json.Unmarshal(elementsRaw, &elements); err != nil {
			return nil, err
		}
		var p rawObject
		_ = json.Unmarshal(panel, &p)
		name := "library-" + strconv.FormatInt(id, 10)
		for elements[name] != nil {
			name += "-new"
		}
		elements[name] = rawValue(rawObject{"kind": rawValue("LibraryPanel"), "spec": rawValue(rawObject{"id": p["id"], "title": p["title"], "libraryPanel": p["libraryPanel"]})})
		object["elements"] = rawValue(elements)
		layout, err := appendLibraryLayout(object["layout"], name, p["gridPos"])
		if err != nil {
			return nil, err
		}
		object["layout"] = layout
	} else {
		var panels []json.RawMessage
		if err := json.Unmarshal(object["panels"], &panels); len(object["panels"]) > 0 && err != nil {
			return nil, err
		}
		object["panels"] = rawValue(append(panels, panel))
	}
	return json.Marshal(object)
}

func appendLibraryLayout(raw json.RawMessage, name string, posRaw json.RawMessage) (json.RawMessage, error) {
	var layout rawObject
	if len(raw) == 0 || string(raw) == "null" {
		layout = rawObject{"kind": rawValue("GridLayout"), "spec": rawValue(rawObject{"items": rawValue([]any{})})}
	} else if err := json.Unmarshal(raw, &layout); err != nil {
		return nil, err
	}
	kind := rawString(layout["kind"])
	var spec rawObject
	_ = json.Unmarshal(layout["spec"], &spec)
	if spec == nil {
		spec = rawObject{}
	}
	switch kind {
	case "GridLayout", "AutoGridLayout":
		var items []json.RawMessage
		_ = json.Unmarshal(spec["items"], &items)
		item := rawObject{"element": rawValue(map[string]string{"kind": "ElementReference", "name": name})}
		if kind == "GridLayout" {
			var pos rawObject
			_ = json.Unmarshal(posRaw, &pos)
			item["x"], item["y"], item["width"], item["height"] = pos["x"], pos["y"], pos["w"], pos["h"]
		}
		items = append(items, rawValue(rawObject{"kind": rawValue(kind + "Item"), "spec": rawValue(item)}))
		spec["items"] = rawValue(items)
	case "RowsLayout", "TabsLayout":
		key, itemKind := "rows", "RowsLayoutRow"
		if kind == "TabsLayout" {
			key, itemKind = "tabs", "TabsLayoutTab"
		}
		var items []rawObject
		_ = json.Unmarshal(spec[key], &items)
		if len(items) == 0 {
			items = append(items, rawObject{"kind": rawValue(itemKind), "spec": rawValue(rawObject{})})
		}
		last := items[len(items)-1]
		var nested rawObject
		_ = json.Unmarshal(last["spec"], &nested)
		if nested == nil {
			nested = rawObject{}
		}
		child, err := appendLibraryLayout(nested["layout"], name, posRaw)
		if err != nil {
			return nil, err
		}
		nested["layout"] = child
		last["spec"] = rawValue(nested)
		items[len(items)-1] = last
		spec[key] = rawValue(items)
	default:
		return nil, fmt.Errorf("cannot insert a library panel into layout %q", kind)
	}
	layout["spec"] = rawValue(spec)
	return json.Marshal(layout)
}
func (s *Server) unlinkDashboardLibrary(w http.ResponseWriter, r *http.Request) {
	var cmd struct {
		PanelID  string `json:"panelId"`
		Revision *int64 `json:"revision"`
	}
	if err := decode(w, r, &cmd); err != nil {
		fail(w, 400, err)
		return
	}
	d, err := s.Store.Dashboard(r.Context(), r.PathValue("id"))
	if err != nil {
		dashboardResourceError(w, err)
		return
	}
	if cmd.Revision == nil {
		cmd.Revision = &d.UpdatedAt
	}
	d, err = s.resolveDashboardLibrary(r.Context(), d)
	if err != nil {
		dashboardResourceError(w, err)
		return
	}
	found := false
	sourceID := json.RawMessage(nil)
	var standalone json.RawMessage
	for i, p := range d.Panels {
		if p.ID != cmd.PanelID {
			continue
		}
		var config rawObject
		_ = json.Unmarshal(p.Config, &config)
		if len(config["libraryPanel"]) == 0 {
			fail(w, 400, errors.New("panel is not linked to a library"))
			return
		}
		sourceID = config["id"]
		delete(config, "libraryPanel")
		standalone = rawValue(config)
		d.Panels[i].Config = standalone
		found = true
		break
	}
	if !found {
		fail(w, 404, errors.New("panel not found"))
		return
	}
	if len(d.Grafana) > 0 && string(d.Grafana) != "null" {
		d.Grafana, err = transformRaw(d.Grafana, func(object rawObject) (rawObject, bool, error) {
			if rawString(object["kind"]) == "LibraryPanel" {
				var spec rawObject
				_ = json.Unmarshal(object["spec"], &spec)
				if string(spec["id"]) == string(sourceID) {
					var panel rawObject
					_ = json.Unmarshal(standalone, &panel)
					converted := classicLibraryV2(panel, spec)
					var next rawObject
					_ = json.Unmarshal(converted["spec"], &next)
					delete(next, "libraryPanel")
					for key, value := range spec {
						if key != "libraryPanel" {
							if _, exists := next[key]; !exists {
								next[key] = value
							}
						}
					}
					converted["spec"] = rawValue(next)
					for key, value := range object {
						if key != "kind" && key != "spec" {
							converted[key] = value
						}
					}
					return converted, true, nil
				}
			}
			if len(object["libraryPanel"]) > 0 && len(object["type"]) > 0 && string(object["id"]) == string(sourceID) {
				var panel rawObject
				_ = json.Unmarshal(standalone, &panel)
				return panel, true, nil
			}
			return object, false, nil
		})
		if err != nil {
			dashboardResourceError(w, err)
			return
		}
	}
	d, err = s.saveDashboardWithLibraryPanels(r.Context(), d, cmd.Revision)
	if err != nil {
		dashboardResourceError(w, err)
		return
	}
	writeJSON(w, 200, d)
}

package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"reflect"
	"strconv"
	"strings"

	"github.com/neko233-com/MetricsPanel233/internal/model"
)

type panelQueryDraft struct {
	OriginalRef *string         `json:"originalRef,omitempty"`
	Baseline    json.RawMessage `json:"baseline,omitempty"`
	Query       json.RawMessage `json:"query"`
}

type panelQueriesDraft struct {
	ID      string            `json:"id"`
	Queries []panelQueryDraft `json:"queries"`
}

func (s *Server) dashboardQueryRoutes(api routeRegistrar) {
	api.HandleFunc("PATCH /api/v1/dashboards/{id}/queries", func(w http.ResponseWriter, r *http.Request) {
		match := strings.TrimSpace(r.Header.Get("If-Match"))
		if len(match) >= 2 && match[0] == '"' && match[len(match)-1] == '"' {
			match = match[1 : len(match)-1]
		}
		revision, err := strconv.ParseInt(match, 10, 64)
		if match == "" {
			fail(w, 428, errors.New("If-Match dashboard revision is required"))
			return
		}
		if err != nil || revision <= 0 {
			fail(w, 400, errors.New("If-Match must contain a positive dashboard revision"))
			return
		}
		var input struct {
			Panels []panelQueriesDraft `json:"panels"`
		}
		if err := decode(w, r, &input); err != nil {
			fail(w, 400, err)
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
			fail(w, 409, errors.New("dashboard changed; reload before saving queries"))
			return
		}
		d, err = applyDashboardQueries(d, input.Panels)
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

// Baselines describe the browser's view of a query, including JSON.parse's number
// rounding. Unchanged fields are copied from the stored raw query instead of
// writing the browser's copy back over plugin settings or wide numeric literals.
func browserJSON(value any) any {
	switch v := value.(type) {
	case json.Number:
		n, _ := strconv.ParseFloat(string(v), 64)
		if math.IsInf(n, 0) || math.IsNaN(n) {
			return nil
		}
		return n
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, child := range v {
			out[key] = browserJSON(child)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, child := range v {
			out[i] = browserJSON(child)
		}
		return out
	default:
		return v
	}
}

func sameQueryJSON(a, b json.RawMessage) bool {
	x, err := historyJSON(a)
	if err != nil {
		return false
	}
	y, err := historyJSON(b)
	return err == nil && len(diffDashboardJSON(x, y)) == 0
}

func queryJSONKey(raw json.RawMessage) string {
	value, err := historyJSON(raw)
	if err != nil {
		return ""
	}
	return string(rawValue(value))
}

func mergeQueryEdits(original, baseline, draft json.RawMessage) json.RawMessage {
	if sameQueryJSON(baseline, draft) {
		return original
	}
	var o, b, n rawObject
	if json.Unmarshal(original, &o) == nil && o != nil && json.Unmarshal(baseline, &b) == nil && b != nil && json.Unmarshal(draft, &n) == nil && n != nil {
		for key := range b {
			if _, exists := n[key]; !exists {
				delete(o, key)
			}
		}
		for key, value := range n {
			if old, exists := b[key]; exists && len(o[key]) > 0 {
				o[key] = mergeQueryEdits(o[key], old, value)
			} else {
				o[key] = value
			}
		}
		return rawValue(o)
	}
	var oa, ba, na []json.RawMessage
	if json.Unmarshal(original, &oa) == nil && oa != nil && json.Unmarshal(baseline, &ba) == nil && ba != nil && json.Unmarshal(draft, &na) == nil && na != nil && len(oa) == len(ba) {
		used := map[int]bool{}
		exact, identities := map[string][]int{}, map[string][]int{}
		cursor := map[string]int{}
		for i, old := range ba {
			key := queryJSONKey(old)
			exact[key] = append(exact[key], i)
			var item rawObject
			_ = json.Unmarshal(old, &item)
			for _, name := range []string{"id", "refId", "name"} {
				if len(item[name]) > 0 {
					key := name + ":" + queryJSONKey(item[name])
					identities[key] = append(identities[key], i)
				}
			}
		}
		find := func(kind, key string, entries []int) int {
			position := cursor[kind+key]
			for position < len(entries) && used[entries[position]] {
				position++
			}
			cursor[kind+key] = position
			if position < len(entries) {
				return entries[position]
			}
			return -1
		}
		for i, value := range na {
			key := queryJSONKey(value)
			found := find("exact:", key, exact[key])
			if found >= 0 {
				used[found] = true
				na[i] = oa[found]
				continue
			}
			// Match changed objects by their stable query/filter identity when an
			// array is reordered. Otherwise an unchanged-length array edits in place.
			var next rawObject
			_ = json.Unmarshal(value, &next)
			for _, identity := range []string{"id", "refId", "name"} {
				if len(next[identity]) == 0 {
					continue
				}
				key := identity + ":" + queryJSONKey(next[identity])
				found = find("identity:", key, identities[key])
				if found >= 0 {
					break
				}
			}
			if found < 0 && len(ba) == len(na) && !used[i] {
				found = i
			}
			if found >= 0 {
				used[found] = true
				na[i] = mergeQueryEdits(oa[found], ba[found], value)
			}
		}
		return rawValue(na)
	}
	return draft
}

func applyDashboardQueries(d model.Dashboard, edits []panelQueriesDraft) (model.Dashboard, error) {
	if len(edits) < 1 || len(edits) > len(d.Panels) {
		return d, errors.New("queries must name one or more existing panels")
	}
	d.Panels = append([]model.Panel(nil), d.Panels...)
	seen := map[string]bool{}
	for _, edit := range edits {
		if seen[edit.ID] {
			return d, errors.New("duplicate panel in query update")
		}
		seen[edit.ID] = true
		index := -1
		for i, panel := range d.Panels {
			if panel.ID == edit.ID {
				index = i
				break
			}
		}
		if index < 0 {
			return d, fmt.Errorf("panel %q does not exist", edit.ID)
		}
		panel := &d.Panels[index]
		config := rawObject{}
		if len(panel.Config) > 0 && json.Unmarshal(panel.Config, &config) != nil {
			return d, errors.New("invalid stored panel config")
		}
		if config == nil {
			config = rawObject{}
		}
		if len(config["libraryPanel"]) > 0 && string(config["libraryPanel"]) != "null" {
			return d, errors.New("edit the shared library model before changing a linked panel's queries")
		}
		var original []json.RawMessage
		if len(config["targets"]) > 0 && json.Unmarshal(config["targets"], &original) != nil {
			return d, errors.New("invalid stored panel queries")
		}
		if len(edit.Queries) > 32 {
			return d, errors.New("at most 32 panel queries")
		}
		originals := map[string]json.RawMessage{}
		for _, raw := range original {
			var q rawObject
			if json.Unmarshal(raw, &q) != nil || q == nil {
				return d, errors.New("invalid stored query")
			}
			ref := rawString(q["refId"])
			if _, duplicate := originals[ref]; duplicate {
				return d, errors.New("stored query references are ambiguous")
			}
			originals[ref] = raw
		}
		targets := make([]json.RawMessage, 0, len(edit.Queries))
		refs, reused := map[string]bool{}, map[string]bool{}
		for _, draft := range edit.Queries {
			var query rawObject
			if json.Unmarshal(draft.Query, &query) != nil || query == nil {
				return d, errors.New("query must be a JSON object")
			}
			ref := rawString(query["refId"])
			if strings.TrimSpace(ref) == "" || len(ref) > 100 || refs[ref] {
				return d, errors.New("queries require unique references of 1–100 characters")
			}
			refs[ref] = true
			raw := draft.Query
			if draft.OriginalRef != nil {
				old, exists := originals[*draft.OriginalRef]
				if !exists || reused[*draft.OriginalRef] {
					return d, errors.New("query original reference is missing or duplicated")
				}
				reused[*draft.OriginalRef] = true
				x, xerr := historyJSON(old)
				y, yerr := historyJSON(draft.Baseline)
				if xerr != nil || yerr != nil || !reflect.DeepEqual(browserJSON(x), browserJSON(y)) {
					return d, errors.New("query baseline differs from the stored query; reload before editing")
				}
				raw = mergeQueryEdits(old, draft.Baseline, draft.Query)
			} else if len(draft.Baseline) > 0 {
				return d, errors.New("a new query must not include a baseline")
			}
			targets = append(targets, raw)
		}
		if len(d.Grafana) > 0 {
			var err error
			d.Grafana, err = patchSourceQueries(d.Grafana, config, targets, edit.Queries)
			if err != nil {
				return d, err
			}
		}
		config["targets"] = rawValue(targets)
		panel.Config = rawValue(config)
		panel.Expressions = []string{}
		panel.Expr = ""
		for _, raw := range targets {
			var q struct {
				Expr string `json:"expr"`
				Hide bool   `json:"hide"`
			}
			if err := json.Unmarshal(raw, &q); err != nil {
				return d, errors.New("expr/hide query fields have invalid types")
			}
			if q.Expr != "" && !q.Hide {
				panel.Expressions = append(panel.Expressions, q.Expr)
			}
		}
		if len(panel.Expressions) > 0 {
			panel.Expr = panel.Expressions[0]
		}
	}
	return d, d.Validate()
}

func patchSourceQueries(raw json.RawMessage, config rawObject, targets []json.RawMessage, drafts []panelQueryDraft) (json.RawMessage, error) {
	var doc rawObject
	if json.Unmarshal(raw, &doc) != nil || doc == nil {
		return nil, errors.New("invalid Grafana source")
	}
	for _, wrapper := range []string{"dashboard", "spec"} {
		if source, exists := doc[wrapper]; exists {
			next, err := patchSourceQueries(source, config, targets, drafts)
			if err != nil {
				return nil, err
			}
			doc[wrapper] = next
			return json.Marshal(doc)
		}
	}
	id := string(config["id"])
	edited := 0
	if value, exists := doc["elements"]; exists {
		if id == "" {
			return nil, errors.New("V2 panel needs a unique source ID for query editing")
		}
		var elements map[string]rawObject
		if err := json.Unmarshal(value, &elements); err != nil {
			return nil, err
		}
		for _, element := range elements {
			var spec rawObject
			_ = json.Unmarshal(element["spec"], &spec)
			if rawString(element["kind"]) != "Panel" || string(spec["id"]) != id {
				continue
			}
			edited++
			var group, data rawObject
			_ = json.Unmarshal(spec["data"], &group)
			if group == nil {
				group = rawObject{"kind": rawValue("QueryGroup")}
			}
			_ = json.Unmarshal(group["spec"], &data)
			if data == nil {
				data = rawObject{}
			}
			var previous []rawObject
			_ = json.Unmarshal(data["queries"], &previous)
			originals := map[string]rawObject{}
			for _, query := range previous {
				var item rawObject
				_ = json.Unmarshal(query["spec"], &item)
				originals[rawString(item["refId"])] = query
			}
			queries := make([]rawObject, 0, len(targets))
			for i, raw := range targets {
				envelope := rawObject{"kind": rawValue("PanelQuery")}
				if drafts[i].OriginalRef != nil {
					if old, ok := originals[*drafts[i].OriginalRef]; ok {
						if sameQueryJSON(drafts[i].Baseline, drafts[i].Query) {
							queries = append(queries, old)
							continue
						}
						envelope = old
					}
				}
				var qspec, query, target rawObject
				_ = json.Unmarshal(envelope["spec"], &qspec)
				if qspec == nil {
					qspec = rawObject{}
				}
				_ = json.Unmarshal(qspec["query"], &query)
				if query == nil {
					query = rawObject{"kind": rawValue("DataQuery")}
				}
				_ = json.Unmarshal(raw, &target)
				qspec["refId"] = target["refId"]
				if hide, ok := target["hide"]; ok {
					qspec["hidden"] = hide
				} else {
					delete(qspec, "hidden")
				}
				var baseline, draft rawObject
				_ = json.Unmarshal(drafts[i].Baseline, &baseline)
				_ = json.Unmarshal(drafts[i].Query, &draft)
				beforeDS, afterDS := baseline["datasource"], draft["datasource"]
				unchangedDS := drafts[i].OriginalRef != nil && ((len(beforeDS) == 0 && len(afterDS) == 0) || sameQueryJSON(beforeDS, afterDS))
				if !unchangedDS {
					ds := target["datasource"]
					var reference rawObject
					_ = json.Unmarshal(query["datasource"], &reference)
					if reference == nil {
						reference = rawObject{}
					}
					uid := ""
					var source rawObject
					if json.Unmarshal(ds, &source) == nil && source != nil {
						if value := source["uid"]; len(value) > 0 && json.Unmarshal(value, &uid) != nil {
							return nil, errors.New("query datasource uid must be a string")
						}
						kind := ""
						if value := source["type"]; len(value) > 0 && json.Unmarshal(value, &kind) != nil {
							return nil, errors.New("query datasource type must be a string")
						}
						if kind != "" {
							query["group"] = rawValue(kind)
						}
					} else if len(ds) > 0 && json.Unmarshal(ds, &uid) != nil {
						return nil, errors.New("query datasource must be a reference, string or null")
					}
					// An omitted/null/empty UID clears the binding. Keep opaque
					// reference metadata instead of retaining the previous name.
					reference["name"] = rawValue(uid)
					query["datasource"] = rawValue(reference)
				}
				delete(target, "refId")
				delete(target, "hide")
				delete(target, "datasource")
				query["spec"] = rawValue(target)
				qspec["query"] = rawValue(query)
				envelope["spec"] = rawValue(qspec)
				queries = append(queries, envelope)
			}
			data["queries"] = rawValue(queries)
			group["spec"] = rawValue(data)
			spec["data"] = rawValue(group)
			element["spec"] = rawValue(spec)
		}
		doc["elements"] = rawValue(elements)
	} else {
		var walk func(json.RawMessage) (json.RawMessage, error)
		walk = func(value json.RawMessage) (json.RawMessage, error) {
			var panels []rawObject
			if err := json.Unmarshal(value, &panels); err != nil {
				return nil, err
			}
			for _, panel := range panels {
				matches := id != "" && string(panel["id"]) == id
				if id == "" {
					matches = sameQueryJSON(rawValue(panel), rawValue(config))
				}
				if matches {
					panel["targets"] = rawValue(targets)
					edited++
				}
				if children, ok := panel["panels"]; ok {
					next, err := walk(children)
					if err != nil {
						return nil, err
					}
					panel["panels"] = next
				}
			}
			return json.Marshal(panels)
		}
		if value, ok := doc["panels"]; ok {
			next, err := walk(value)
			if err != nil {
				return nil, err
			}
			doc["panels"] = next
		}
		if value, ok := doc["rows"]; ok {
			var rows []rawObject
			if err := json.Unmarshal(value, &rows); err != nil {
				return nil, err
			}
			for _, row := range rows {
				if panels, ok := row["panels"]; ok {
					next, err := walk(panels)
					if err != nil {
						return nil, err
					}
					row["panels"] = next
				}
			}
			doc["rows"] = rawValue(rows)
		}
	}
	if edited != 1 {
		return nil, errors.New("cannot identify a unique Grafana source panel")
	}
	return json.Marshal(doc)
}

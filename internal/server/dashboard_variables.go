package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"

	"github.com/neko233-com/MetricsPanel233/internal/model"
)

type dashboardVariableDraft struct {
	OriginalName *string         `json:"originalName,omitempty"`
	Baseline     json.RawMessage `json:"baseline,omitempty"`
	Variable     json.RawMessage `json:"variable"`
}

func (s *Server) dashboardVariableRoutes(api *http.ServeMux) {
	api.HandleFunc("PATCH /api/v1/dashboards/{id}/variables", func(w http.ResponseWriter, r *http.Request) {
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
			Variables json.RawMessage `json:"variables"`
		}
		if err := decode(w, r, &input); err != nil {
			fail(w, 400, err)
			return
		}
		var drafts []dashboardVariableDraft
		if len(input.Variables) == 0 || json.Unmarshal(input.Variables, &drafts) != nil || drafts == nil {
			fail(w, 400, errors.New("variables must be the complete desired JSON array"))
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
			fail(w, 409, errors.New("dashboard changed; reload before saving variables"))
			return
		}
		d, err = applyDashboardVariables(d, drafts)
		if err != nil {
			fail(w, 400, err)
			return
		}
		// Variable edits never update a library panel definition. The store still
		// writes the dashboard revision, connection references and history atomically.
		d, err = s.Store.SaveDashboardAtRevision(ctx, d, revision)
		if err != nil {
			dashboardResourceError(w, err)
			return
		}
		writeJSON(w, 200, d)
	})
}

var variableKinds = map[string]string{
	"query": "QueryVariable", "custom": "CustomVariable", "constant": "ConstantVariable",
	"textbox": "TextVariable", "datasource": "DatasourceVariable", "interval": "IntervalVariable",
	"adhoc": "AdhocVariable", "groupby": "GroupByVariable",
}

func applyDashboardVariables(d model.Dashboard, drafts []dashboardVariableDraft) (model.Dashboard, error) {
	if len(drafts) > 32 {
		return d, errors.New("at most 32 variables")
	}
	originals := make(map[string]model.Variable, len(d.Variables))
	for _, variable := range d.Variables {
		if _, duplicate := originals[variable.Name]; duplicate {
			return d, errors.New("stored variable names are ambiguous")
		}
		originals[variable.Name] = variable
	}
	variables := make([]model.Variable, 0, len(drafts))
	used, names := map[string]bool{}, map[string]bool{}
	for _, draft := range drafts {
		var object rawObject
		if json.Unmarshal(draft.Variable, &object) != nil || object == nil || len(draft.Variable) > 192*1024 {
			return d, errors.New("variable must be a JSON object under 192 KiB")
		}
		var original model.Variable
		if draft.OriginalName != nil {
			var exists bool
			original, exists = originals[*draft.OriginalName]
			if !exists || used[*draft.OriginalName] {
				return d, errors.New("variable original name is missing or duplicated")
			}
			used[*draft.OriginalName] = true
			x, xerr := historyJSON(rawValue(original))
			y, yerr := historyJSON(draft.Baseline)
			if xerr != nil || yerr != nil || !reflect.DeepEqual(browserJSON(x), browserJSON(y)) {
				return d, errors.New("variable baseline differs from the stored variable; reload before editing")
			}
		} else if len(draft.Baseline) > 0 {
			return d, errors.New("a new variable must not include a baseline")
		}
		next, err := mergeDashboardVariable(original, draft)
		if err != nil {
			return d, err
		}
		if next.Name == "" || len(next.Name) > 100 || names[next.Name] {
			return d, errors.New("variables require unique names of 1–100 characters")
		}
		if variableKinds[next.Type] == "" && (draft.OriginalName == nil || next.Type != original.Type) {
			return d, errors.New("unsupported new variable type")
		}
		if len(next.Query) > 128*1024 || len(next.Current) > 10000 || len(next.Options) > 1000 {
			return d, errors.New("variable query, default value or options exceed their limit")
		}
		for _, option := range next.Options {
			if len(option) > 10000 {
				return d, errors.New("variable option exceeds 10000 bytes")
			}
		}
		names[next.Name] = true
		variables = append(variables, next)
	}
	if len(d.Grafana) > 0 && string(d.Grafana) != "null" {
		var err error
		d.Grafana, variables, err = patchDashboardDocumentVariables(d.Grafana, drafts, variables)
		if err != nil {
			return d, err
		}
	}
	d.Variables = variables
	return d, d.Validate()
}

func variableObject(raw json.RawMessage) (rawObject, error) {
	if len(raw) == 0 {
		return rawObject{}, nil
	}
	var config rawObject
	if json.Unmarshal(raw, &config) != nil || config == nil {
		return nil, errors.New("variable config must be a JSON object")
	}
	return config, nil
}

func variableText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) == nil && list != nil {
		values := make([]string, len(list))
		for i, value := range list {
			values[i] = variableText(value)
		}
		return strings.Join(values, "|")
	}
	if string(raw) == "true" || string(raw) == "false" {
		return string(raw)
	}
	if len(raw) > 0 && string(raw) != "null" {
		var number json.Number
		if json.Unmarshal(raw, &number) == nil {
			return string(number)
		}
	}
	return ""
}

func variableQuery(raw json.RawMessage) string {
	if text := rawString(raw); text != "" {
		return text
	}
	var object rawObject
	if json.Unmarshal(raw, &object) == nil && object != nil {
		for _, key := range []string{"rawSql", "query", "expr"} {
			if text := rawString(object[key]); text != "" {
				return text
			}
		}
	}
	return ""
}

func sameVariableBrowserJSON(a, b json.RawMessage) bool {
	x, xerr := historyJSON(a)
	y, yerr := historyJSON(b)
	return xerr == nil && yerr == nil && reflect.DeepEqual(browserJSON(x), browserJSON(y))
}

func mergeDashboardVariable(original model.Variable, draft dashboardVariableDraft) (model.Variable, error) {
	var next, before model.Variable
	if draft.OriginalName != nil {
		if json.Unmarshal(draft.Baseline, &before) != nil {
			return next, errors.New("invalid variable baseline")
		}
	}
	raw := draft.Variable
	if draft.OriginalName != nil {
		raw = mergeQueryEdits(rawValue(original), draft.Baseline, raw)
	}
	if json.Unmarshal(raw, &next) != nil {
		return next, errors.New("invalid variable model")
	}
	config, err := variableObject(next.Config)
	if err != nil {
		return next, err
	}
	baseline, err := variableObject(before.Config)
	if err != nil {
		return next, err
	}
	newVariable := draft.OriginalName == nil
	missingNativeConfig := len(before.Config) == 0
	configChanged := func(key string) bool {
		old, existed := baseline[key]
		value, exists := config[key]
		return existed != exists || exists && !sameVariableBrowserJSON(old, value)
	}
	// Explicit config edits take priority; native semantic fields also support
	// CLI clients that do not synthesize Grafana's config object themselves.
	for _, field := range []struct{ key, old, value string }{{"name", before.Name, next.Name}, {"type", before.Type, next.Type}} {
		if !configChanged(field.key) && (newVariable || field.old != field.value || missingNativeConfig && len(config[field.key]) == 0) {
			config[field.key] = rawValue(field.value)
		}
		if configChanged(field.key) || newVariable {
			if rawString(config[field.key]) == "" {
				return next, errors.New("variable config requires name and type")
			}
		}
	}
	next.Name, next.Type = rawString(config["name"]), rawString(config["type"])
	if !configChanged("query") && (newVariable || next.Query != before.Query || missingNativeConfig && len(config["query"]) == 0) {
		var query rawObject
		if json.Unmarshal(config["query"], &query) == nil && query != nil {
			key := "query"
			for _, candidate := range []string{"rawSql", "query", "expr"} {
				if _, exists := query[candidate]; exists {
					key = candidate
					break
				}
			}
			query[key] = rawValue(next.Query)
			config["query"] = rawValue(query)
		} else {
			config["query"] = rawValue(next.Query)
		}
	}
	if configChanged("query") || newVariable {
		next.Query = variableQuery(config["query"])
	}
	for _, field := range []struct {
		key        string
		old, value bool
	}{{"multi", before.Multi, next.Multi}, {"includeAll", before.IncludeAll, next.IncludeAll}} {
		if !configChanged(field.key) && (newVariable || field.old != field.value || missingNativeConfig && len(config[field.key]) == 0) {
			config[field.key] = rawValue(field.value)
		}
	}
	next.Multi, next.IncludeAll = false, false
	for _, flag := range []struct {
		key   string
		value *bool
	}{{"multi", &next.Multi}, {"includeAll", &next.IncludeAll}} {
		if raw := config[flag.key]; len(raw) > 0 && json.Unmarshal(raw, flag.value) != nil {
			return next, fmt.Errorf("variable %s must be boolean", flag.key)
		}
	}
	if !configChanged("current") && (newVariable || next.Current != before.Current || missingNativeConfig && len(config["current"]) == 0) {
		current, err := variableObject(config["current"])
		if err != nil {
			return next, errors.New("variable current must be a JSON object")
		}
		var value any = next.Current
		var previous []json.RawMessage
		if next.Multi && (json.Unmarshal(current["value"], &previous) == nil && previous != nil || strings.Contains(next.Current, "|")) {
			value = strings.Split(next.Current, "|")
		}
		current["value"], current["text"] = rawValue(value), rawValue(value)
		config["current"] = rawValue(current)
	}
	if configChanged("current") || newVariable {
		current, err := variableObject(config["current"])
		if err != nil {
			return next, errors.New("variable current must be a JSON object")
		}
		next.Current = variableText(current["value"])
	}
	if !configChanged("options") && (newVariable || !reflect.DeepEqual(next.Options, before.Options) || missingNativeConfig && len(config["options"]) == 0) {
		var old []rawObject
		_ = json.Unmarshal(config["options"], &old)
		byValue := map[string]rawObject{}
		for _, option := range old {
			byValue[variableText(option["value"])] = option
		}
		options := make([]rawObject, 0, len(next.Options))
		for _, value := range next.Options {
			option := byValue[value]
			if option == nil {
				option = rawObject{"value": rawValue(value), "text": rawValue(value)}
			}
			options = append(options, option)
		}
		config["options"] = rawValue(options)
	}
	if configChanged("options") || newVariable {
		var options []rawObject
		if len(config["options"]) > 0 && json.Unmarshal(config["options"], &options) != nil {
			return next, errors.New("variable options must be a JSON array of objects")
		}
		next.Options = make([]string, 0, len(options))
		for _, option := range options {
			if option == nil {
				return next, errors.New("variable options must be JSON objects")
			}
			if value := variableText(option["value"]); value != "" && value != "$__all" {
				next.Options = append(next.Options, value)
			}
		}
	}
	if next.Type == "constant" {
		next.Current = next.Query
	}
	next.Config = rawValue(config)
	return next, nil
}

func normalizeV2Variable(raw json.RawMessage) (rawObject, rawObject, error) {
	envelope, err := variableObject(raw)
	if err != nil {
		return nil, nil, err
	}
	spec, err := variableObject(envelope["spec"])
	if err != nil {
		return nil, nil, errors.New("V2 variable requires a spec object")
	}
	config := make(rawObject, len(spec)+2)
	for key, value := range spec {
		config[key] = value
	}
	kind := rawString(envelope["kind"])
	for name, candidate := range variableKinds {
		if candidate == kind {
			config["type"] = rawValue(name)
			break
		}
	}
	if config["type"] == nil {
		config["type"] = rawValue("")
	}
	var query rawObject
	if json.Unmarshal(config["query"], &query) == nil && query != nil {
		var datasource rawObject
		_ = json.Unmarshal(query["datasource"], &datasource)
		if name := rawString(datasource["name"]); name != "" {
			config["datasource"] = rawValue(rawObject{"uid": rawValue(name), "type": query["group"]})
		}
		config["query"] = query["spec"]
		if len(config["query"]) == 0 {
			config["query"] = rawValue(nil)
		}
	}
	if kind == "DatasourceVariable" && rawString(spec["pluginId"]) != "" {
		config["query"] = spec["pluginId"]
	}
	return envelope, config, nil
}

func patchDashboardDocumentVariables(raw json.RawMessage, drafts []dashboardVariableDraft, variables []model.Variable) (json.RawMessage, []model.Variable, error) {
	doc, err := variableObject(raw)
	if err != nil {
		return nil, variables, errors.New("invalid stored Grafana dashboard")
	}
	for _, wrapper := range []string{"dashboard", "spec"} {
		if child, exists := doc[wrapper]; exists {
			patched, next, err := patchDashboardDocumentVariables(child, drafts, variables)
			if err != nil {
				return nil, variables, err
			}
			doc[wrapper] = patched
			return rawValue(doc), next, nil
		}
	}
	_, v2 := doc["elements"]
	var templating rawObject
	var list []json.RawMessage
	if v2 {
		if value := doc["variables"]; len(value) > 0 && json.Unmarshal(value, &list) != nil {
			return nil, variables, errors.New("invalid V2 variable array")
		}
	} else {
		templating, err = variableObject(doc["templating"])
		if err != nil {
			return nil, variables, errors.New("invalid Grafana templating object")
		}
		if value := templating["list"]; len(value) > 0 && json.Unmarshal(value, &list) != nil {
			return nil, variables, errors.New("invalid Grafana variable list")
		}
	}
	type sourceVariable struct{ envelope, config rawObject }
	originals := map[string]sourceVariable{}
	for _, source := range list {
		var item sourceVariable
		if v2 {
			item.envelope, item.config, err = normalizeV2Variable(source)
		} else {
			item.config, err = variableObject(source)
		}
		if err != nil {
			return nil, variables, err
		}
		name := rawString(item.config["name"])
		if name == "" || originals[name].config != nil {
			return nil, variables, errors.New("stored Grafana variable names are missing or ambiguous")
		}
		originals[name] = item
	}
	result := make([]json.RawMessage, 0, len(variables))
	for i := range variables {
		item := sourceVariable{config: rawObject{}}
		if drafts[i].OriginalName != nil {
			var exists bool
			item, exists = originals[*drafts[i].OriginalName]
			if !exists {
				return nil, variables, errors.New("variable original name is absent from its Grafana source")
			}
			var baseline model.Variable
			_ = json.Unmarshal(drafts[i].Baseline, &baseline)
			// V2 import normalization used to round unknown spec numbers. Merge
			// only edits onto the exact original spec, never backfill rounded data.
			variables[i].Config = mergeQueryEdits(rawValue(item.config), baseline.Config, variables[i].Config)
		}
		if !v2 {
			result = append(result, variables[i].Config)
			continue
		}
		envelope, err := patchV2Variable(item.envelope, item.config, variables[i])
		if err != nil {
			return nil, variables, err
		}
		result = append(result, envelope)
	}
	if v2 {
		doc["variables"] = rawValue(result)
	} else {
		templating["list"] = rawValue(result)
		doc["templating"] = rawValue(templating)
	}
	return rawValue(doc), variables, nil
}

func patchV2Variable(envelope, oldConfig rawObject, variable model.Variable) (json.RawMessage, error) {
	newVariable := envelope == nil
	if envelope == nil {
		envelope = rawObject{}
	}
	spec, err := variableObject(envelope["spec"])
	if err != nil {
		return nil, err
	}
	config, err := variableObject(variable.Config)
	if err != nil {
		return nil, err
	}
	kind := variableKinds[variable.Type]
	if kind == "" {
		kind = rawString(envelope["kind"])
		if kind == "" {
			return nil, fmt.Errorf("V2 variable %q needs a supported type", variable.Name)
		}
	}
	before, after := rawObject{}, rawObject{}
	for key, value := range oldConfig {
		if key != "type" && key != "query" && key != "datasource" {
			before[key] = value
		}
	}
	for key, value := range config {
		if key != "type" && key != "query" && key != "datasource" {
			after[key] = value
		}
	}
	if json.Unmarshal(mergeQueryEdits(rawValue(spec), rawValue(before), rawValue(after)), &spec) != nil {
		return nil, errors.New("invalid V2 variable spec")
	}
	changed := func(key string) bool {
		old, existed := oldConfig[key]
		value, exists := config[key]
		return existed != exists || exists && !sameQueryJSON(old, value)
	}
	if kind == "QueryVariable" {
		if changed("query") || changed("datasource") || rawString(envelope["kind"]) != kind {
			query, err := variableObject(spec["query"])
			if err != nil {
				query = rawObject{}
			}
			if len(query["kind"]) == 0 {
				query["kind"] = rawValue("DataQuery")
			}
			if len(query["group"]) == 0 {
				query["group"] = rawValue("prometheus")
			}
			if changed("query") || rawString(envelope["kind"]) != kind {
				query["spec"] = config["query"]
				if len(query["spec"]) == 0 {
					query["spec"] = rawValue("")
				}
			}
			if changed("datasource") {
				var source rawObject
				if len(config["datasource"]) == 0 || string(config["datasource"]) == "null" {
					delete(query, "datasource")
				} else if json.Unmarshal(config["datasource"], &source) == nil && source != nil {
					reference, err := variableObject(query["datasource"])
					if err != nil {
						return nil, err
					}
					if uid := source["uid"]; len(uid) > 0 {
						reference["name"] = uid
					}
					query["datasource"] = rawValue(reference)
					if group := source["type"]; len(group) > 0 {
						query["group"] = group
					}
				} else if uid := rawString(config["datasource"]); uid != "" {
					reference, _ := variableObject(query["datasource"])
					reference["name"] = rawValue(uid)
					query["datasource"] = rawValue(reference)
				} else {
					return nil, errors.New("variable datasource must be a UID string or reference object")
				}
			}
			spec["query"] = rawValue(query)
		}
	} else if kind == "DatasourceVariable" {
		if changed("query") || rawString(envelope["kind"]) != kind {
			spec["pluginId"] = rawValue(variable.Query)
		}
	} else if kind == "AdhocVariable" || kind == "GroupByVariable" {
		if changed("datasource") {
			var source rawObject
			if json.Unmarshal(config["datasource"], &source) == nil && source != nil {
				reference, _ := variableObject(envelope["datasource"])
				if reference == nil {
					reference = rawObject{}
				}
				if uid := source["uid"]; len(uid) > 0 {
					reference["name"] = uid
				}
				envelope["datasource"] = rawValue(reference)
				if group := source["type"]; len(group) > 0 {
					envelope["group"] = group
				}
			} else if uid := rawString(config["datasource"]); uid != "" {
				reference, _ := variableObject(envelope["datasource"])
				if reference == nil {
					reference = rawObject{}
				}
				reference["name"] = rawValue(uid)
				envelope["datasource"] = rawValue(reference)
			} else if len(config["datasource"]) == 0 || string(config["datasource"]) == "null" {
				delete(envelope, "datasource")
			} else {
				return nil, errors.New("variable datasource must be a UID string or reference object")
			}
		}
		if newVariable && len(envelope["group"]) == 0 {
			envelope["group"] = rawValue("prometheus")
		}
	} else if changed("query") || rawString(envelope["kind"]) != kind {
		if value, exists := config["query"]; exists {
			spec["query"] = value
		} else {
			delete(spec, "query")
		}
	}
	if err := completeV2VariableSpec(spec, kind, newVariable || rawString(envelope["kind"]) != kind, variable); err != nil {
		return nil, err
	}
	envelope["kind"], envelope["spec"] = rawValue(kind), rawValue(spec)
	return rawValue(envelope), nil
}

func completeV2VariableSpec(spec rawObject, kind string, initialize bool, variable model.Variable) error {
	for _, field := range []struct {
		key    string
		values []string
	}{
		{"hide", []string{"dontHide", "hideLabel", "hideVariable", "inControlsMenu"}},
		{"refresh", []string{"never", "onDashboardLoad", "onTimeRangeChanged"}},
		{"sort", []string{"disabled", "alphabeticalAsc", "alphabeticalDesc", "numericalAsc", "numericalDesc", "alphabeticalCaseInsensitiveAsc", "alphabeticalCaseInsensitiveDesc", "naturalAsc", "naturalDesc"}},
	} {
		if raw := spec[field.key]; len(raw) > 0 && rawString(raw) == "" {
			var value int
			if json.Unmarshal(raw, &value) != nil || value < 0 || value >= len(field.values) {
				return fmt.Errorf("invalid V2 variable %s setting", field.key)
			}
			spec[field.key] = rawValue(field.values[value])
		}
	}
	if !initialize {
		return nil
	}
	set := func(key string, value any) {
		if len(spec[key]) == 0 {
			spec[key] = rawValue(value)
		}
	}
	set("hide", "dontHide")
	set("skipUrlSync", false)
	if kind == "AdhocVariable" {
		for _, key := range []string{"current", "options", "multi", "includeAll", "query"} {
			delete(spec, key)
		}
		set("baseFilters", []any{})
		set("filters", []any{})
		set("defaultKeys", []any{})
		set("allowCustomValue", false)
		return nil
	}
	set("current", rawObject{"value": rawValue(variable.Current), "text": rawValue(variable.Current)})
	switch kind {
	case "TextVariable", "ConstantVariable":
		for _, key := range []string{"options", "multi", "includeAll", "regex", "refresh", "sort", "allowCustomValue"} {
			delete(spec, key)
		}
	case "IntervalVariable":
		delete(spec, "multi")
		delete(spec, "includeAll")
		set("options", []any{})
		set("auto", false)
		set("auto_min", "10s")
		set("auto_count", 30)
		spec["refresh"] = rawValue("onTimeRangeChanged")
	case "GroupByVariable":
		delete(spec, "query")
		delete(spec, "includeAll")
		set("options", []any{})
		set("multi", variable.Multi)
	case "CustomVariable", "QueryVariable", "DatasourceVariable":
		set("options", []any{})
		set("multi", variable.Multi)
		set("includeAll", variable.IncludeAll)
		set("allowCustomValue", false)
		if kind == "QueryVariable" || kind == "DatasourceVariable" {
			set("regex", "")
			set("refresh", "onDashboardLoad")
		}
		if kind == "QueryVariable" {
			set("sort", "disabled")
		} else if kind == "DatasourceVariable" {
			delete(spec, "query")
		}
	}
	return nil
}

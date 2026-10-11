package grafana

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

type DashboardInput struct {
	Name     string `json:"name"`
	Label    string `json:"label"`
	Type     string `json:"type"`
	PluginID string `json:"plugin_id,omitempty"`
	Value    string `json:"value,omitempty"`
	Required bool   `json:"required"`
}

var inputPlaceholder = regexp.MustCompile(`\$\{([^{}]+)\}`)

func dashboardObject(data []byte) (map[string]any, map[string]any, error) {
	if len(data) == 0 || len(data) > CommunityBodyLimit {
		return nil, nil, errors.New("dashboard JSON must be under 4 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var root map[string]any
	if err := decoder.Decode(&root); err != nil || root == nil {
		return nil, nil, errors.New("dashboard must be a JSON object")
	}
	if !json.Valid(data) {
		return nil, nil, errors.New("dashboard must contain one valid JSON document")
	}
	document := root
	if nested, exists := root["dashboard"]; exists {
		var ok bool
		document, ok = nested.(map[string]any)
		if !ok || document == nil {
			return nil, nil, errors.New("dashboard wrapper needs an object")
		}
	}
	if nested, exists := document["spec"]; exists {
		var ok bool
		document, ok = nested.(map[string]any)
		if !ok || document == nil {
			return nil, nil, errors.New("dashboard resource needs an object spec")
		}
	}
	if title, _ := document["title"].(string); title == "" {
		return nil, nil, errors.New("Grafana dashboard title required")
	}
	return root, document, nil
}

// PreviewInputs reports explicit portable-export inputs and datasource variables.
// Public V2 export labels retain distinct same-type datasource selections.
func PreviewInputs(data []byte) (string, []DashboardInput, error) {
	_, document, err := dashboardObject(data)
	if err != nil {
		return "", nil, err
	}
	inputs := []DashboardInput{}
	seen := map[string]int{}
	labeledVariables := map[string]bool{}
	add := func(input DashboardInput) error {
		if input.Name == "" || len(input.Name) > 256 || (input.Type != "datasource" && input.Type != "constant") || (input.Type == "datasource" && input.PluginID == "") {
			return errors.New("invalid dashboard import input")
		}
		if input.PluginID == "__expr__" || input.PluginID == "grafana" {
			return nil
		}
		if index, ok := seen[input.Name]; ok {
			old := inputs[index]
			if old.Type != input.Type || old.PluginID != input.PluginID {
				return fmt.Errorf("input %q has conflicting definitions", input.Name)
			}
			return nil
		}
		if len(inputs) >= 128 {
			return errors.New("at most 128 dashboard import inputs")
		}
		if input.Label == "" {
			input.Label = input.Name
		}
		seen[input.Name] = len(inputs)
		inputs = append(inputs, input)
		return nil
	}
	if raw, present := document["__inputs"]; present {
		definitions, ok := raw.([]any)
		if !ok {
			return "", nil, errors.New("__inputs must be an array")
		}
		for _, raw := range definitions {
			definition, ok := raw.(map[string]any)
			if !ok {
				return "", nil, errors.New("__inputs entries must be objects")
			}
			if _, exists := seen[inputString(definition["name"])]; exists {
				return "", nil, fmt.Errorf("duplicate dashboard input %q", inputString(definition["name"]))
			}
			if err := add(DashboardInput{Name: inputString(definition["name"]), Label: inputString(definition["label"]), Type: inputString(definition["type"]), PluginID: inputString(definition["pluginId"]), Value: inputString(definition["value"]), Required: true}); err != nil {
				return "", nil, err
			}
		}
	}
	if err := walkInputObjects(document, func(node map[string]any) error {
		variable := variableObject(node)
		if variable != nil {
			kind := inputString(variable["type"])
			if node["kind"] == "DatasourceVariable" {
				kind = "datasource"
			}
			if kind == "datasource" {
				current, _ := variable["current"].(map[string]any)
				return add(DashboardInput{Name: inputString(variable["name"]), Label: inputString(variable["label"]), Type: "datasource", PluginID: datasourceVariablePlugin(node, variable), Value: inputString(current["value"]), Required: false})
			}
			if node["kind"] == "ConstantVariable" {
				return add(DashboardInput{Name: inputString(variable["name"]), Label: inputString(variable["label"]), Type: "constant", Value: inputString(variable["query"]), Required: false})
			}
		}
		labels, _ := node["labels"].(map[string]any)
		if label := inputString(labels["grafana.app/export-label"]); label != "" && inputString(node["group"]) != "" {
			if ref := inputString(object(node["datasource"])["name"]); strings.HasPrefix(ref, "$") {
				labeledVariables[strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(ref, "$"), "{"), "}")] = true
			}
			return add(DashboardInput{Name: label, Label: inputString(labels["grafana.app/export-datasource-name"]), Type: "datasource", PluginID: inputString(node["group"]), Required: true})
		}
		return nil
	}); err != nil {
		return "", nil, err
	}
	filtered := inputs[:0]
	for _, input := range inputs {
		if input.Type == "datasource" && !input.Required && labeledVariables[input.Name] {
			continue
		}
		filtered = append(filtered, input)
	}
	return inputString(document["title"]), filtered, nil
}

// BindDashboard changes only configured import placeholders, variable selections
// and requested identity. Opaque fields and resource envelopes remain intact.
func BindDashboard(data []byte, bindings map[string]string, name, uid string) ([]byte, error) {
	_, inputs, err := PreviewInputs(data)
	if err != nil {
		return nil, err
	}
	known := map[string]DashboardInput{}
	for _, input := range inputs {
		known[input.Name] = input
		if _, ok := bindings[input.Name]; !ok && input.Required {
			return nil, fmt.Errorf("dashboard input %q requires a value", input.Name)
		}
	}
	for key, value := range bindings {
		if _, ok := known[key]; !ok {
			return nil, fmt.Errorf("unknown dashboard input %q", key)
		}
		if len(value) > 16384 {
			return nil, fmt.Errorf("dashboard input %q exceeds 16384 bytes", key)
		}
	}
	root, document, err := dashboardObject(data)
	if err != nil {
		return nil, err
	}
	edits, variableBindings, err := planDatasourceRebindings(document, bindings)
	if err != nil {
		return nil, err
	}
	replacements := map[string]string{}
	inputValues := []datasourceFieldEdit{}
	for _, raw := range array(document["__inputs"]) {
		definition := object(raw)
		key := inputString(definition["name"])
		if pluginID := inputString(definition["pluginId"]); pluginID == "__expr__" || pluginID == "grafana" {
			replacements[key] = pluginID
		} else {
			replacements[key] = bindings[key]
		}
		inputValues = append(inputValues, datasourceFieldEdit{definition, "value", replacements[key]})
	}
	var replaceValue func(any) any
	replaceValue = func(value any) any {
		switch typed := value.(type) {
		case string:
			return inputPlaceholder.ReplaceAllStringFunc(typed, func(match string) string {
				if replacement, ok := replacements[match[2:len(match)-1]]; ok {
					return replacement
				}
				return match
			})
		case []any:
			for index := range typed {
				typed[index] = replaceValue(typed[index])
			}
		case map[string]any:
			for key, item := range typed {
				typed[key] = replaceValue(item)
			}
		}
		return value
	}
	replaceValue(root)
	// Apply changes selected from the original references once, after placeholder
	// expansion. Newly chosen UIDs and literal input defaults must not expand again.
	for _, edit := range append(edits, inputValues...) {
		edit.owner[edit.key] = edit.value
	}
	if err := walkInputObjects(document, func(node map[string]any) error {
		labels, _ := node["labels"].(map[string]any)
		label := inputString(labels["grafana.app/export-label"])
		value, exists := bindings[label]
		if !exists && (node["group"] == "__expr__" || node["group"] == "grafana") {
			value, exists = inputString(node["group"]), label != ""
		}
		if exists {
			datasource := object(node["datasource"])
			ref := inputString(datasource["name"])
			if strings.HasPrefix(ref, "$") {
				key := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(ref, "$"), "{"), "}")
				if previous, ok := variableBindings[key]; ok && previous != value {
					return fmt.Errorf("datasource variable %q has conflicting mappings", key)
				}
				if explicit, ok := bindings[key]; ok && explicit != value {
					return fmt.Errorf("datasource variable %q conflicts with its export-label mapping", key)
				}
				variableBindings[key] = value
			} else {
				datasource["name"] = value
				node["datasource"] = datasource
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	for _, node := range dashboardVariableNodes(document) {
		variable := variableObject(node)
		if variable == nil {
			continue
		}
		key := inputString(variable["name"])
		value, exists := bindings[key]
		if !exists {
			value, exists = variableBindings[key]
		}
		if !exists {
			continue
		}
		current := object(variable["current"])
		current["text"], current["value"] = value, value
		variable["current"] = current
		if node["kind"] == "ConstantVariable" {
			variable["query"] = value
		}
		options := array(variable["options"])
		matched := false
		for _, raw := range options {
			option := object(raw)
			selected := option["value"] == value
			option["selected"] = selected
			matched = matched || selected
		}
		if !matched {
			options = append(options, map[string]any{"text": value, "value": value, "selected": true})
		}
		variable["options"] = options
	}
	if name != "" {
		if len(name) > 100 {
			return nil, errors.New("dashboard name must contain 1–100 bytes")
		}
		document["title"] = name
	}
	if uid != "" {
		if len(uid) > 128 || strings.ContainsAny(uid, "/?#\\") {
			return nil, errors.New("invalid dashboard UID")
		}
		resource := root
		if wrapped, ok := root["dashboard"].(map[string]any); ok {
			resource = wrapped
		}
		if resource["spec"] != nil {
			metadata := object(resource["metadata"])
			metadata["name"] = uid
			resource["metadata"] = metadata
		} else if document["elements"] != nil {
			// A raw V2 spec has no UID property. Use its public resource envelope
			// when the caller assigns an identity, keeping the spec unchanged.
			envelope := map[string]any{"apiVersion": "dashboard.grafana.app/v2beta1", "kind": "Dashboard", "metadata": map[string]any{"name": uid}, "spec": document}
			if resource != nil && root["dashboard"] != nil {
				root["dashboard"] = envelope
			} else {
				root = envelope
			}
		}
		if document["elements"] == nil {
			document["uid"] = uid
		}
	}
	result, err := json.Marshal(root)
	if len(result) > CommunityBodyLimit {
		return nil, errors.New("bound dashboard exceeds 4 MiB")
	}
	return result, err
}

func inputString(value any) string { text, _ := value.(string); return text }

func datasourceVariablePlugin(node, variable map[string]any) string {
	if node["kind"] == "DatasourceVariable" {
		if pluginID := inputString(variable["pluginId"]); pluginID != "" {
			return pluginID
		}
	}
	return inputString(variable["query"])
}

func dashboardVariableNodes(document map[string]any) []map[string]any {
	nodes := []map[string]any{}
	for _, raw := range array(object(document["templating"])["list"]) {
		nodes = append(nodes, object(raw))
	}
	for _, raw := range array(document["variables"]) {
		nodes = append(nodes, object(raw))
	}
	return nodes
}

type datasourceFieldEdit struct {
	owner map[string]any
	key   string
	value string
}

// Retained declarations identify previously bound sources. Only schema-defined
// references may follow that mapping; queries and opaque plugin fields never do.
func planDatasourceRebindings(document map[string]any, bindings map[string]string) ([]datasourceFieldEdit, map[string]string, error) {
	type sourceMapping struct{ pluginID, old, value string }
	mappings := []sourceMapping{}
	for _, raw := range array(document["__inputs"]) {
		definition := object(raw)
		value, exists := bindings[inputString(definition["name"])]
		old := inputString(definition["value"])
		if exists && definition["type"] == "datasource" && old != "" {
			mappings = append(mappings, sourceMapping{inputString(definition["pluginId"]), old, value})
		}
	}
	resolve := func(old, pluginID string) (string, bool, error) {
		if old == "" || strings.HasPrefix(old, "$") {
			return "", false, nil
		}
		value, found := "", false
		for _, mapping := range mappings {
			if mapping.old != old || (pluginID != "" && mapping.pluginID != pluginID) {
				continue
			}
			if found && mapping.value != value {
				return "", false, fmt.Errorf("ambiguous datasource reference %q: inputs select different UIDs", old)
			}
			value, found = mapping.value, true
		}
		return value, found, nil
	}
	edits := []datasourceFieldEdit{}
	variables := []map[string]any{}
	labeledVariables := map[string]bool{}
	variableBindings := map[string]string{}
	ref := func(node map[string]any, key, pluginID string) error {
		owner, field := node, "datasource"
		old := inputString(node[field])
		if source, ok := node[field].(map[string]any); ok {
			owner, field = source, key
			old = inputString(source[field])
			if key == "uid" {
				if kind := inputString(source["type"]); kind != "" {
					pluginID = kind
				}
			}
		}
		value, found, err := resolve(old, pluginID)
		if found {
			edits = append(edits, datasourceFieldEdit{owner, field, value})
		}
		return err
	}
	v2Query := func(node map[string]any) error {
		if node["kind"] != "DataQuery" && node["kind"] != "AdhocVariable" && node["kind"] != "GroupByVariable" {
			return nil
		}
		labels := object(node["labels"])
		if _, explicit := bindings[inputString(labels["grafana.app/export-label"])]; explicit {
			if source := inputString(object(node["datasource"])["name"]); strings.HasPrefix(source, "$") {
				labeledVariables[strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(source, "$"), "{"), "}")] = true
			}
			return nil // The explicit export-label mapping disambiguates this reference.
		}
		return ref(node, "name", inputString(node["group"]))
	}
	var panels func([]any) error
	panels = func(raws []any) error {
		for _, raw := range raws {
			panel := object(raw)
			if err := ref(panel, "uid", ""); err != nil {
				return err
			}
			for _, raw := range array(panel["targets"]) {
				if err := ref(object(raw), "uid", ""); err != nil {
					return err
				}
			}
			if err := panels(array(panel["panels"])); err != nil {
				return err
			}
		}
		return nil
	}
	if err := panels(array(document["panels"])); err != nil {
		return nil, nil, err
	}
	for _, raw := range array(document["rows"]) {
		if err := panels(array(object(raw)["panels"])); err != nil {
			return nil, nil, err
		}
	}
	for _, raw := range array(object(document["templating"])["list"]) {
		variable := object(raw)
		if err := ref(variable, "uid", ""); err != nil {
			return nil, nil, err
		}
		if variable["type"] == "datasource" {
			variables = append(variables, variable)
		}
	}
	for _, raw := range object(document["elements"]) {
		panel := object(raw)
		if panel["kind"] != "Panel" {
			continue
		}
		data := object(object(panel["spec"])["data"])
		for _, raw := range array(object(data["spec"])["queries"]) {
			if err := v2Query(object(object(object(raw)["spec"])["query"])); err != nil {
				return nil, nil, err
			}
		}
	}
	for _, raw := range array(document["variables"]) {
		node := object(raw)
		variable := object(node["spec"])
		if node["kind"] == "DatasourceVariable" {
			// Preserve the spec; the synthetic type is only used while choosing a mapping.
			variables = append(variables, map[string]any{"name": variable["name"], "query": datasourceVariablePlugin(node, variable), "current": variable["current"]})
		}
		if err := v2Query(node); err != nil {
			return nil, nil, err
		}
		if node["kind"] == "QueryVariable" {
			if err := v2Query(object(variable["query"])); err != nil {
				return nil, nil, err
			}
		}
	}
	for _, raw := range array(object(document["annotations"])["list"]) {
		if err := ref(object(raw), "uid", ""); err != nil {
			return nil, nil, err
		}
	}
	for _, raw := range array(document["annotations"]) {
		annotation := object(raw)
		if annotation["kind"] == "AnnotationQuery" {
			if err := v2Query(object(object(annotation["spec"])["query"])); err != nil {
				return nil, nil, err
			}
		}
	}
	for _, variable := range variables {
		name := inputString(variable["name"])
		if _, explicit := bindings[name]; explicit || labeledVariables[name] {
			continue
		}
		old := inputString(object(variable["current"])["value"])
		value, found, err := resolve(old, inputString(variable["query"]))
		if err != nil {
			return nil, nil, err
		}
		if found {
			variableBindings[name] = value
		}
	}
	return edits, variableBindings, nil
}

func variableObject(node map[string]any) map[string]any {
	if node["kind"] == "DatasourceVariable" || node["kind"] == "ConstantVariable" {
		variable, _ := node["spec"].(map[string]any)
		return variable
	}
	if node["type"] == "datasource" && node["name"] != nil && node["query"] != nil {
		return node
	}
	return nil
}

func walkInputObjects(value any, visit func(map[string]any) error) error {
	switch typed := value.(type) {
	case map[string]any:
		if err := visit(typed); err != nil {
			return err
		}
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if err := walkInputObjects(typed[key], visit); err != nil {
				return err
			}
		}
	case []any:
		for _, item := range typed {
			if err := walkInputObjects(item, visit); err != nil {
				return err
			}
		}
	}
	return nil
}

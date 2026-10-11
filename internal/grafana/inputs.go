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
				return add(DashboardInput{Name: inputString(variable["name"]), Label: inputString(variable["label"]), Type: "datasource", PluginID: inputString(variable["query"]), Value: inputString(current["value"]), Required: false})
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
	replacements := map[string]string{}
	for _, raw := range array(document["__inputs"]) {
		definition := object(raw)
		key := inputString(definition["name"])
		if pluginID := inputString(definition["pluginId"]); pluginID == "__expr__" || pluginID == "grafana" {
			replacements[key] = pluginID
		} else {
			replacements[key] = bindings[key]
		}
		// Keep the portable input declaration for export, with its chosen value
		// so reimport can offer the same binding instead of losing that context.
		definition["value"] = replacements[key]
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
	variableBindings := map[string]string{}
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
	_ = walkInputObjects(document, func(node map[string]any) error {
		variable := variableObject(node)
		if variable == nil {
			return nil
		}
		key := inputString(variable["name"])
		value, exists := bindings[key]
		if !exists {
			value, exists = variableBindings[key]
		}
		if !exists {
			return nil
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
		return nil
	})
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

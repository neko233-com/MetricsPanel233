package grafana

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// ImportInput matches Grafana's ordered legacy import request, including its
// wildcard name. PluginID is descriptive; matching uses the name and type.
type ImportInput struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	PluginID string `json:"pluginId"`
	Value    string `json:"value"`
}

var ErrDashboardInputMissing = errors.New("missing dashboard input variable")

func importDocument(data []byte) (map[string]any, map[string]any, error) {
	if len(data) == 0 || len(data) > CommunityBodyLimit || !json.Valid(data) {
		return nil, nil, errors.New("dashboard must be a valid JSON object under 4 MiB")
	}
	var root map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&root); err != nil || root == nil {
		return nil, nil, errors.New("dashboard must be a JSON object")
	}
	doc := root
	if nested, exists := doc["dashboard"]; exists {
		var ok bool
		doc, ok = nested.(map[string]any)
		if !ok || doc == nil {
			return nil, nil, errors.New("dashboard wrapper needs an object")
		}
	}
	if nested, exists := doc["spec"]; exists {
		var ok bool
		doc, ok = nested.(map[string]any)
		if !ok || doc == nil {
			return nil, nil, errors.New("dashboard resource needs an object spec")
		}
	}
	return root, doc, nil
}

// Interpolate follows Grafana 13.2.3's legacy evaluator. Library elements remain
// available to the import service; read-only API responses strip them afterward.
func Interpolate(data []byte, inputs []ImportInput) ([]byte, error) {
	root, doc, err := importDocument(data)
	if err != nil {
		return nil, err
	}
	if len(inputs) > 128 {
		return nil, errors.New("at most 128 dashboard import inputs")
	}
	values := map[string]string{}
	definitions, _ := doc["__inputs"].([]any)
	for _, raw := range definitions {
		definition := object(raw)
		name, kind := inputString(definition["name"]), inputString(definition["type"])
		if definition["pluginId"] == "__expr__" {
			values[name] = "__expr__"
			continue
		}
		matched := false
		for _, input := range inputs {
			if input.Type == kind && (input.Name == name || input.Name == "*") {
				if len(input.Value) > 16384 {
					return nil, fmt.Errorf("dashboard input %q exceeds 16384 bytes", name)
				}
				values[name], matched = input.Value, true
				break
			}
		}
		if !matched {
			return nil, fmt.Errorf("dashboard import failed: %w %s", ErrDashboardInputMissing, name)
		}
	}
	var expand func(any) any
	expand = func(value any) any {
		switch v := value.(type) {
		case string:
			return inputPlaceholder.ReplaceAllStringFunc(v, func(match string) string {
				if chosen, exists := values[match[2:len(match)-1]]; exists {
					return chosen
				}
				return match
			})
		case []any:
			for i := range v {
				v[i] = expand(v[i])
			}
		case map[string]any:
			delete(v, "__inputs")
			for key := range v {
				v[key] = expand(v[key])
			}
		}
		return value
	}
	expand(root)
	result, err := json.Marshal(root)
	if len(result) > CommunityBodyLimit {
		return nil, errors.New("interpolated dashboard exceeds 4 MiB")
	}
	return result, err
}

func StripImportMetadata(data []byte) ([]byte, error) {
	root, doc, err := importDocument(data)
	if err != nil {
		return nil, err
	}
	delete(doc, "__inputs")
	delete(doc, "__requires")
	delete(doc, "__elements")
	return json.Marshal(root)
}

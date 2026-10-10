package main

import (
	"encoding/json"
	"errors"
	"fmt"
)

func previewAlertCLI(client apiClient, file string, at int64) error {
	data, err := readFile(file)
	if err != nil {
		return err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil || object == nil {
		return errors.New("preview file must be a Grafana graph or native graph rule JSON object")
	}
	graph := json.RawMessage(data)
	if raw, exists := object["grafana"]; exists {
		graph = raw
	}
	result, err := client.call("POST", "/api/v1/alerts/preview", map[string]any{"grafana": graph, "at": at})
	if err != nil {
		return err
	}
	if err = output(json.RawMessage(result)); err != nil {
		return err
	}
	var preview struct {
		Error   string `json:"condition_error"`
		Results map[string]struct {
			Error string `json:"error"`
		} `json:"results"`
	}
	if err = json.Unmarshal(result, &preview); err != nil {
		return err
	}
	if preview.Error != "" {
		return fmt.Errorf("alert preview: %s", preview.Error)
	}
	for ref, response := range preview.Results {
		if response.Error != "" {
			return fmt.Errorf("alert preview query %s: %s", ref, response.Error)
		}
	}
	return nil
}

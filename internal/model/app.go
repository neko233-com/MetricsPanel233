package model

import (
	"encoding/json"
	"errors"
)

type AppSettings struct {
	ID               string          `json:"id"`
	OrgID            int64           `json:"orgId"`
	Enabled          bool            `json:"enabled"`
	Pinned           bool            `json:"pinned"`
	JSONData         json.RawMessage `json:"jsonData"`
	SecureJSONFields map[string]bool `json:"secureJsonFields"`
	Version          int             `json:"version"`
	UpdatedAt        int64           `json:"updated_at"`
}
type AppSettingsInput struct {
	Enabled          *bool             `json:"enabled,omitempty"`
	Pinned           *bool             `json:"pinned,omitempty"`
	JSONData         json.RawMessage   `json:"jsonData,omitempty"`
	SecureJSONData   map[string]string `json:"secureJsonData,omitempty"`
	SecureJSONFields map[string]bool   `json:"secureJsonFields,omitempty"`
	Version          *int              `json:"version,omitempty"`
}

func (a AppSettingsInput) Validate() error {
	if len(a.JSONData) > 0 {
		var object map[string]any
		if len(a.JSONData) > 128*1024 || json.Unmarshal(a.JSONData, &object) != nil || object == nil {
			return errors.New("jsonData must be an object under 128 KiB")
		}
	}
	if len(a.SecureJSONData) > 32 || len(a.SecureJSONFields) > 32 {
		return errors.New("at most 32 secret fields")
	}
	for key, value := range a.SecureJSONData {
		if len(key) == 0 || len(key) > 100 || len(value) > 16384 {
			return errors.New("secret field key/value limit: 100/16384 bytes")
		}
	}
	return nil
}

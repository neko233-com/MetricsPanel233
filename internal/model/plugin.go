package model

import (
	"encoding/json"
	"errors"
	"regexp"
)

var PluginID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,127}$`)

type Plugin struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Type        string            `json:"type"`
	Version     string            `json:"version"`
	Backend     bool              `json:"backend"`
	Executable  string            `json:"executable,omitempty"`
	Enabled     bool              `json:"enabled"`
	SHA256      string            `json:"sha256"`
	Signature   string            `json:"signature"`
	InstalledAt int64             `json:"installed_at"`
	Metadata    json.RawMessage   `json:"metadata"`
	PackageID   string            `json:"package_id"`
	AssetPath   string            `json:"asset_path"`
	Files       map[string]string `json:"files,omitempty"`
}

type DataSource struct {
	ID               int64           `json:"id"`
	UID              string          `json:"uid"`
	OrgID            int64           `json:"orgId"`
	Name             string          `json:"name"`
	Type             string          `json:"type"`
	Access           string          `json:"access"`
	URL              string          `json:"url"`
	User             string          `json:"user,omitempty"`
	Database         string          `json:"database,omitempty"`
	BasicAuth        bool            `json:"basicAuth"`
	BasicAuthUser    string          `json:"basicAuthUser,omitempty"`
	IsDefault        bool            `json:"isDefault"`
	ReadOnly         bool            `json:"readOnly,omitempty"`
	JSONData         json.RawMessage `json:"jsonData"`
	SecureJSONFields map[string]bool `json:"secureJsonFields"`
	Version          int             `json:"version"`
	UpdatedAt        int64           `json:"updated_at"`
}
type DataSourceInput struct {
	DataSource
	SecureJSONData map[string]string `json:"secureJsonData,omitempty"`
}

func (d *DataSource) Defaults() {
	if d.OrgID == 0 {
		d.OrgID = 1
	}
	if d.Access == "" {
		d.Access = "proxy"
	}
	if len(d.JSONData) == 0 {
		d.JSONData = json.RawMessage(`{}`)
	}
}
func (d DataSourceInput) Validate() error {
	if !alertUID.MatchString(d.UID) || d.UID == "metricspanel" || d.UID == "grafana" || d.UID == "-1" || d.Name == "-- Grafana --" || d.Type == "grafana" || d.ReadOnly || !PluginID.MatchString(d.Type) || len(d.Name) == 0 || len(d.Name) > 256 || d.OrgID != 1 || d.Access != "proxy" {
		return errors.New("datasource needs a unique UID, name, plugin type and proxy access in organization 1")
	}
	if len(d.URL) > 4096 || len(d.User) > 512 || len(d.Database) > 512 || len(d.BasicAuthUser) > 512 {
		return errors.New("datasource URL/user/database fields exceed limits")
	}
	var data map[string]any
	if len(d.JSONData) > 128*1024 || json.Unmarshal(d.JSONData, &data) != nil || data == nil {
		return errors.New("jsonData must be an object under 128 KiB")
	}
	if len(d.SecureJSONData) > 32 || len(d.SecureJSONFields) > 32 {
		return errors.New("at most 32 secret fields")
	}
	for key, value := range d.SecureJSONData {
		if len(key) == 0 || len(key) > 100 || len(value) > 16384 {
			return errors.New("secret field key/value limit: 100/16384 bytes")
		}
	}
	return nil
}

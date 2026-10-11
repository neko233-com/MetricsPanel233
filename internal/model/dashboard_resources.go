package model

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
)

var ResourceUID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,40}$`)

func (d Dashboard) UID() string {
	var object map[string]json.RawMessage
	if json.Unmarshal(d.Grafana, &object) == nil {
		if wrapped := object["dashboard"]; len(wrapped) > 0 {
			var document map[string]json.RawMessage
			if json.Unmarshal(wrapped, &document) != nil || document == nil {
				return d.ID
			}
			object = document
		}
		var uid string
		if json.Unmarshal(object["uid"], &uid) == nil && uid != "" {
			return uid
		}
		var metadata struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(object["metadata"], &metadata)
		if metadata.Name != "" {
			return metadata.Name
		}
		var spec struct {
			UID string `json:"uid"`
		}
		_ = json.Unmarshal(object["spec"], &spec)
		if spec.UID != "" {
			return spec.UID
		}
	}
	return d.ID
}

func (d Dashboard) SourceVersion() int64 {
	var object map[string]json.RawMessage
	if json.Unmarshal(d.Grafana, &object) != nil || object == nil {
		return 1
	}
	if wrapped := object["dashboard"]; len(wrapped) > 0 {
		var document map[string]json.RawMessage
		if json.Unmarshal(wrapped, &document) != nil || document == nil {
			return 1
		}
		object = document
	}
	var metadata struct {
		Generation int64 `json:"generation"`
	}
	_ = json.Unmarshal(object["metadata"], &metadata)
	var apiVersion string
	_ = json.Unmarshal(object["apiVersion"], &apiVersion)
	if spec := object["spec"]; len(spec) > 0 {
		var document map[string]json.RawMessage
		if json.Unmarshal(spec, &document) != nil || document == nil {
			return 1
		}
		object = document
	}
	// V2 resources use generation; Classic and V1 retain their document version.
	v2 := strings.Contains(apiVersion, "/v2") || len(object["elements"]) > 0 && string(object["elements"]) != "null"
	if v2 && metadata.Generation > 0 {
		return metadata.Generation
	}
	var version int64
	_ = json.Unmarshal(object["version"], &version)
	if version > 0 {
		return version
	}
	if metadata.Generation > 0 {
		return metadata.Generation
	}
	return 1
}

func RootFolderUID(uid string) string {
	if uid == "general" {
		return ""
	}
	return uid
}

type Folder struct {
	ID          int64     `json:"id"`
	UID         string    `json:"uid"`
	Title       string    `json:"title"`
	ParentUID   string    `json:"parentUid,omitempty"`
	Description string    `json:"description"`
	Version     int64     `json:"version"`
	Created     time.Time `json:"created"`
	Updated     time.Time `json:"updated"`
	URL         string    `json:"url"`
	CanSave     bool      `json:"canSave"`
	CanEdit     bool      `json:"canEdit"`
	CanAdmin    bool      `json:"canAdmin"`
	CanDelete   bool      `json:"canDelete"`
}

func (f Folder) Validate() error {
	if !ResourceUID.MatchString(f.UID) || f.UID == "general" || f.UID == "sharedwithme" {
		return errors.New("folder UID must contain 1–40 letters, numbers, underscores or hyphens and cannot be reserved")
	}
	if strings.TrimSpace(f.Title) == "" || len(f.Title) > 256 {
		return errors.New("folder title must contain 1–256 bytes")
	}
	if len(f.Description) > 8192 {
		return errors.New("folder description exceeds 8192 bytes")
	}
	return nil
}

type LibraryElementUser struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatarUrl"`
}
type LibraryElementMeta struct {
	FolderName          string             `json:"folderName"`
	FolderUID           string             `json:"folderUid"`
	ConnectedDashboards int64              `json:"connectedDashboards"`
	Created             time.Time          `json:"created"`
	Updated             time.Time          `json:"updated"`
	CreatedBy           LibraryElementUser `json:"createdBy"`
	UpdatedBy           LibraryElementUser `json:"updatedBy"`
}
type LibraryElement struct {
	ID          int64              `json:"id"`
	OrgID       int64              `json:"orgId"`
	FolderID    int64              `json:"folderId"`
	FolderUID   string             `json:"folderUid"`
	UID         string             `json:"uid"`
	Name        string             `json:"name"`
	Kind        int64              `json:"kind"`
	Type        string             `json:"type"`
	Description string             `json:"description"`
	Model       json.RawMessage    `json:"model"`
	Version     int64              `json:"version"`
	Meta        LibraryElementMeta `json:"meta"`
}

func (e LibraryElement) Validate() error {
	if !ResourceUID.MatchString(e.UID) {
		return errors.New("library panel UID must contain 1–40 letters, numbers, underscores or hyphens")
	}
	if e.Kind != 1 {
		return errors.New("library element kind must be 1 (panel)")
	}
	if strings.TrimSpace(e.Name) == "" || len(e.Name) > 256 {
		return errors.New("library panel name must contain 1–256 bytes")
	}
	var object map[string]json.RawMessage
	if len(e.Model) > 512*1024 || json.Unmarshal(e.Model, &object) != nil || object == nil {
		return errors.New("library panel model must be a JSON object under 512 KiB")
	}
	var panelType string
	if json.Unmarshal(object["type"], &panelType) != nil || panelType == "" {
		return errors.New("library panel model needs a visualization type")
	}
	return nil
}

type LibraryConnection struct {
	ID            int64              `json:"id"`
	Kind          int64              `json:"kind"`
	ElementID     int64              `json:"elementId"`
	ConnectionID  int64              `json:"connectionId"`
	ConnectionUID string             `json:"connectionUid"`
	DashboardUID  string             `json:"dashboardUid"`
	DashboardID   int64              `json:"dashboardId"`
	Name          string             `json:"name"`
	URL           string             `json:"url"`
	Created       time.Time          `json:"created"`
	CreatedBy     LibraryElementUser `json:"createdBy"`
}

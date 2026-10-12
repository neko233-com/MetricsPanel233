package model

import (
	"errors"
	"strconv"
	"strings"
)

// ResourcePermissionLevel follows Grafana's named levels, not arbitrary bit flags.
type ResourcePermissionLevel int

const (
	PermissionNone  ResourcePermissionLevel = 0
	PermissionView  ResourcePermissionLevel = 1
	PermissionEdit  ResourcePermissionLevel = 2
	PermissionAdmin ResourcePermissionLevel = 4
)

func (p ResourcePermissionLevel) ValidGrant() bool {
	return p == PermissionView || p == PermissionEdit || p == PermissionAdmin
}

func (p ResourcePermissionLevel) String() string {
	switch p {
	case PermissionView:
		return "View"
	case PermissionEdit:
		return "Edit"
	case PermissionAdmin:
		return "Admin"
	default:
		return ""
	}
}

func (p ResourcePermissionLevel) Includes(required ResourcePermissionLevel) bool {
	return p.ValidGrant() && required.ValidGrant() && p >= required
}

type PermissionResourceKind string

const (
	PermissionFolder    PermissionResourceKind = "folder"
	PermissionDashboard PermissionResourceKind = "dashboard"
)

// UID is a real folder UID or the stable native Dashboard.ID. HTTP adapters
// resolve Grafana's external dashboard UID to that stored identity first.
type PermissionResource struct {
	OrgID int64                  `json:"orgId"`
	Kind  PermissionResourceKind `json:"kind"`
	UID   string                 `json:"uid"`
}

func (r PermissionResource) Validate() error {
	if r.OrgID <= 0 || r.UID == "" || len(r.UID) > 512 || strings.TrimSpace(r.UID) != r.UID || strings.ContainsAny(r.UID, "\x00\r\n*") {
		return errors.New("invalid permission resource")
	}
	switch r.Kind {
	case PermissionFolder:
		if !ResourceUID.MatchString(r.UID) || r.UID == "general" || r.UID == "sharedwithme" {
			return errors.New("permission resource must reference a real folder")
		}
	case PermissionDashboard:
	default:
		return errors.New("invalid permission resource kind")
	}
	return nil
}

type PermissionSubjectKind string

const (
	PermissionUser           PermissionSubjectKind = "user"
	PermissionServiceAccount PermissionSubjectKind = "service-account"
	PermissionTeam           PermissionSubjectKind = "team"
	PermissionRole           PermissionSubjectKind = "role"
)

// Human and service-account IDs are independent sequences and stay typed.
type ResourcePermissionSubject struct {
	Kind PermissionSubjectKind `json:"kind"`
	ID   int64                 `json:"id,omitempty"`
	Role string                `json:"role,omitempty"`
}

func (s ResourcePermissionSubject) Validate() error {
	switch s.Kind {
	case PermissionUser, PermissionServiceAccount, PermissionTeam:
		if s.ID <= 0 || s.Role != "" {
			return errors.New("permission subject requires one positive typed identity")
		}
	case PermissionRole:
		if s.ID != 0 || (s.Role != "Viewer" && s.Role != "Editor" && s.Role != "Admin") {
			return errors.New("permission role must be Viewer, Editor or Admin")
		}
	default:
		return errors.New("invalid permission subject kind")
	}
	return nil
}

func (s ResourcePermissionSubject) Key() string {
	if s.Kind == PermissionRole {
		return s.Role
	}
	return strconv.FormatInt(s.ID, 10)
}

type ResourcePermissionGrant struct {
	Subject            ResourcePermissionSubject `json:"subject"`
	Permission         ResourcePermissionLevel   `json:"permission"`
	IdentityGeneration int64                     `json:"identity_generation,omitempty"`
}

type ResourcePermissionDocument struct {
	Resource            PermissionResource        `json:"resource"`
	Version             int64                     `json:"version"`
	DefaultsInitialized bool                      `json:"defaults_initialized"`
	Items               []ResourcePermissionGrant `json:"items"`
}

type ResourcePermissionCreationPolicy string

const (
	// Root-only defaults match the App Platform creation path. Nested resources inherit.
	PermissionRootDefaults ResourcePermissionCreationPolicy = "root-only"
	// Legacy creation additionally grants a nonprovisioned nested creator Admin.
	PermissionLegacyDefaults ResourcePermissionCreationPolicy = "legacy"
)

type ResourcePermissionSeed struct {
	Policy      ResourcePermissionCreationPolicy `json:"policy"`
	Creator     *ResourcePermissionSubject       `json:"creator,omitempty"`
	Provisioned bool                             `json:"provisioned"`
}

type ResolvedResourcePermissionGrant struct {
	ResourcePermissionGrant
	Source    PermissionResource `json:"source"`
	Inherited bool               `json:"inherited"`
}

type EffectiveResourcePermission struct {
	Resource   PermissionResource                `json:"resource"`
	Permission ResourcePermissionLevel           `json:"permission"`
	Items      []ResolvedResourcePermissionGrant `json:"items"`
	// Versions include each ACL and actual folder ancestry revision used.
	Versions map[string]int64 `json:"versions"`
}

// Cleanup is an internal durable identity job. Replay with different fields is rejected.
type ResourcePermissionCleanup struct {
	JobID              string                    `json:"job_id"`
	OrgID              int64                     `json:"orgId"`
	Subject            ResourcePermissionSubject `json:"subject"`
	IdentityVersion    int64                     `json:"identity_version"`
	IdentityGeneration int64                     `json:"identity_generation"`
}

func (j ResourcePermissionCleanup) Validate() error {
	if j.OrgID <= 0 || j.JobID == "" || len(j.JobID) > 256 || strings.ContainsAny(j.JobID, "\x00\r\n") || j.IdentityVersion <= 0 || j.IdentityGeneration <= 0 {
		return errors.New("invalid permission cleanup job")
	}
	if j.Subject.Kind == PermissionRole {
		return errors.New("identity cleanup cannot delete role grants")
	}
	return j.Subject.Validate()
}

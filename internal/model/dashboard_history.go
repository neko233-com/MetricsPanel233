package model

import "time"

// DashboardVersion is an immutable saved dashboard revision. Snapshot retains
// native settings and the original Classic/V1/V2 source without JSON coercion.
type DashboardVersion struct {
	ID            int64     `json:"id"`
	DashboardID   int64     `json:"dashboardId"`
	DashboardUID  string    `json:"uid"`
	ParentVersion int64     `json:"parentVersion"`
	RestoredFrom  int64     `json:"restoredFrom"`
	Version       int64     `json:"version"`
	Created       time.Time `json:"created"`
	CreatedBy     string    `json:"createdBy"`
	Message       string    `json:"message"`
	Snapshot      Dashboard `json:"-"`
}

type DashboardVersionList struct {
	Versions      []DashboardVersion `json:"versions"`
	ContinueToken string             `json:"continueToken"`
}

// DashboardHistoryMetadata describes this save, not the restored source's
// author or parent. ParentVersion always comes from the persisted current row.
type DashboardHistoryMetadata struct {
	Message      string
	CreatedBy    string
	RestoredFrom int64
}

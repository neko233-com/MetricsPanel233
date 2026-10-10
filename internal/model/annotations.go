package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

type Annotation struct {
	ID           int64           `json:"id"`
	DashboardUID string          `json:"dashboardUID,omitempty"`
	DashboardID  int64           `json:"dashboardId,omitempty"`
	PanelID      int64           `json:"panelId,omitempty"`
	UserID       int64           `json:"userId"`
	Login        string          `json:"login"`
	Time         int64           `json:"time"`
	TimeEnd      int64           `json:"timeEnd"`
	Text         string          `json:"text"`
	Tags         []string        `json:"tags"`
	Data         json.RawMessage `json:"data,omitempty"`
	Created      int64           `json:"created"`
	Updated      int64           `json:"updated"`
}

var ErrInvalidAnnotation = errors.New("invalid annotation")

func (a *Annotation) Normalize() (err error) {
	defer func() {
		if err != nil {
			err = fmt.Errorf("%w: %s", ErrInvalidAnnotation, err)
		}
	}()
	if a.Time <= 0 || a.Time > 9007199254740991 {
		return errors.New("annotation time must be positive Unix milliseconds")
	}
	if a.TimeEnd == 0 {
		a.TimeEnd = a.Time
	}
	if a.TimeEnd < a.Time || a.TimeEnd > 9007199254740991 {
		return errors.New("annotation timeEnd must be at least time")
	}
	if strings.TrimSpace(a.Text) == "" || len(a.Text) > 8192 {
		return errors.New("annotation text must contain 1–8192 bytes")
	}
	if len(a.Tags) > 32 || len(a.DashboardUID) > 128 || a.PanelID < 0 {
		return errors.New("invalid annotation scope or too many tags")
	}
	if a.PanelID != 0 && a.DashboardUID == "" {
		return errors.New("panel annotation requires dashboardUID")
	}
	tags := map[string]bool{}
	for _, tag := range a.Tags {
		if strings.TrimSpace(tag) == "" || len(tag) > 256 {
			return errors.New("annotation tags must contain 1–256 bytes")
		}
		tags[tag] = true
	}
	a.Tags = []string{}
	for tag := range tags {
		a.Tags = append(a.Tags, tag)
	}
	sort.Strings(a.Tags)
	if len(a.Data) > 65536 || (len(a.Data) > 0 && !json.Valid(a.Data)) {
		return errors.New("annotation data must be JSON under 64 KiB")
	}
	return nil
}

type AnnotationQuery struct {
	From, To, ID, PanelID, UserID int64
	DashboardUID                  string
	Tags                          []string
	MatchAny                      bool
	Limit                         int
}

// Pointers distinguish omitted properties from a supplied empty tag list.
type AnnotationPatch struct {
	Time    *int64          `json:"time,omitempty"`
	TimeEnd *int64          `json:"timeEnd,omitempty"`
	Text    *string         `json:"text,omitempty"`
	Tags    *[]string       `json:"tags,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
}

package model

import (
	"errors"
	"regexp"
	"strings"
)

// UID identifies a rule across edits. Name is the native display name; Grafana
// metadata.name maps to UID and remains immutable.
type InhibitionRule struct {
	UID            string                `json:"uid"`
	Name           string                `json:"name"`
	Disabled       bool                  `json:"disabled"`
	SourceMatchers []NotificationMatcher `json:"source_matchers"`
	TargetMatchers []NotificationMatcher `json:"target_matchers"`
	Equal          []string              `json:"equal"`
	Version        int                   `json:"version"`
	UpdatedAt      int64                 `json:"updated_at"`
}

var inhibitionUID = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]{0,61}[a-z0-9])?$`)
var inhibitionDNSLabel = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`)

func (r InhibitionRule) Validate() error {
	if !inhibitionUID.MatchString(r.UID) || strings.Contains(r.UID, "..") || strings.TrimSpace(r.Name) == "" || len(r.Name) > 256 {
		return errors.New("inhibition rule requires a lowercase DNS-style UID of 1–63 bytes and a name of 1–256 bytes")
	}
	for _, segment := range strings.Split(r.UID, ".") {
		if !inhibitionDNSLabel.MatchString(segment) {
			return errors.New("inhibition UID must contain valid DNS labels")
		}
	}
	return ValidateInhibitionMatchers(r.SourceMatchers, r.TargetMatchers, r.Equal)
}

func ValidateInhibitionMatchers(source, target []NotificationMatcher, equal []string) error {
	if len(source) < 1 || len(source) > 32 || len(target) < 1 || len(target) > 32 || len(equal) > 32 {
		return errors.New("inhibition requires 1–32 matchers on each side and at most 32 equal labels")
	}
	for _, side := range [][]NotificationMatcher{source, target} {
		for _, m := range side {
			if err := m.Validate(); err != nil {
				return err
			}
			if err := ValidateLabels(map[string]string{m.Name: ""}); err != nil {
				return err
			}
		}
	}
	seen := map[string]bool{}
	for _, name := range equal {
		if seen[name] {
			return errors.New("equal label names must be unique")
		}
		seen[name] = true
		if err := ValidateLabels(map[string]string{name: ""}); err != nil {
			return err
		}
	}
	return nil
}

type InhibitionActiveInstance struct {
	RuleUID     string            `json:"rule_uid"`
	InstanceKey string            `json:"instance_key"`
	Labels      map[string]string `json:"labels"`
	State       string            `json:"state"`
	StartedAt   int64             `json:"started_at"`
}

type InhibitionActiveView struct {
	Instances []InhibitionActiveInstance `json:"instances"`
	Total     int                        `json:"total"`
	Truncated bool                       `json:"truncated"`
}

type InhibitionPreviewInput struct {
	SourceMatchers []NotificationMatcher `json:"source_matchers"`
	TargetMatchers []NotificationMatcher `json:"target_matchers"`
	Equal          []string              `json:"equal"`
	Disabled       bool                  `json:"disabled"`
	Labels         *map[string]string    `json:"labels,omitempty"`
	Limit          int                   `json:"limit,omitempty"`
}
type InhibitionPreviewTarget struct {
	Target            InhibitionActiveInstance   `json:"target"`
	Inhibited         bool                       `json:"inhibited"`
	ExcludedSelfMatch bool                       `json:"excluded_self_match"`
	Sources           []InhibitionActiveInstance `json:"sources"`
	SourceCount       int                        `json:"source_count"`
}
type InhibitionPreview struct {
	TargetMatches     bool                       `json:"target_matches"`
	SourceMatches     bool                       `json:"source_matches"`
	Inhibited         bool                       `json:"inhibited"`
	ExcludedSelfMatch bool                       `json:"excluded_self_match"`
	Sources           []InhibitionActiveInstance `json:"sources"`
	SourceCount       int                        `json:"source_count"`
	Targets           []InhibitionPreviewTarget  `json:"targets"`
	TotalTargets      int                        `json:"total_targets"`
	Truncated         bool                       `json:"truncated"`
}

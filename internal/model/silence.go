package model

import (
	"errors"
	"strings"
	"time"
)

type SilenceMatcher struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	IsRegex bool   `json:"isRegex"`
	IsEqual *bool  `json:"isEqual,omitempty"`
}

func (m SilenceMatcher) NotificationMatcher() NotificationMatcher {
	op := "="
	if m.IsRegex {
		op = "=~"
	}
	if m.IsEqual != nil && !*m.IsEqual {
		if m.IsRegex {
			op = "!~"
		} else {
			op = "!="
		}
	}
	return NotificationMatcher{Name: m.Name, Value: m.Value, Operator: op}
}

func ValidateSilenceMatchers(matchers []SilenceMatcher) error {
	if len(matchers) < 1 || len(matchers) > 32 {
		return errors.New("silence requires 1–32 matchers")
	}
	nonEmpty := false
	for _, m := range matchers {
		if strings.ContainsAny(m.Name, "\r\n\x00") {
			return errors.New("invalid silence matcher name")
		}
		matcher := m.NotificationMatcher()
		if err := matcher.Validate(); err != nil {
			return err
		}
		if !matcher.Matches(map[string]string{}) {
			nonEmpty = true
		}
	}
	if !nonEmpty {
		return errors.New("at least one silence matcher must not match an empty label value")
	}
	return nil
}

func SilenceMatches(matchers []SilenceMatcher, labels map[string]string) bool {
	for _, m := range matchers {
		if !m.NotificationMatcher().Matches(labels) {
			return false
		}
	}
	return len(matchers) > 0
}

type Silence struct {
	ID          string            `json:"id,omitempty"`
	Matchers    []SilenceMatcher  `json:"matchers"`
	StartsAt    time.Time         `json:"startsAt"`
	EndsAt      time.Time         `json:"endsAt"`
	CreatedBy   string            `json:"createdBy"`
	Comment     string            `json:"comment"`
	Annotations map[string]string `json:"annotations"`
	UpdatedAt   time.Time         `json:"updatedAt,omitempty"`
	Status      struct {
		State string `json:"state"`
	} `json:"status,omitempty"`
}

func (s Silence) Validate() error {
	if err := ValidateSilenceMatchers(s.Matchers); err != nil {
		return err
	}
	if s.StartsAt.IsZero() || s.EndsAt.IsZero() || !s.EndsAt.After(s.StartsAt) {
		return errors.New("silence end time must be after its start time")
	}
	if strings.TrimSpace(s.CreatedBy) == "" || len(s.CreatedBy) > 256 || strings.TrimSpace(s.Comment) == "" || len(s.Comment) > 4096 {
		return errors.New("silence requires an author (1–256 bytes) and comment (1–4096 bytes)")
	}
	if len(s.Annotations) > 32 {
		return errors.New("silence supports at most 32 annotations")
	}
	for key, value := range s.Annotations {
		if len(key) == 0 || len(key) > 128 || len(value) > 4096 {
			return errors.New("invalid silence annotation")
		}
	}
	return nil
}

func (s *Silence) At(at time.Time) {
	s.Status.State = "active"
	if !at.Before(s.EndsAt) {
		s.Status.State = "expired"
	} else if at.Before(s.StartsAt) {
		s.Status.State = "pending"
	}
}

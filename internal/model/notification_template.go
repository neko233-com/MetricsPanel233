package model

import (
	"errors"
	"strings"
)

type NotificationTemplate struct {
	Name        string   `json:"name"`
	Template    string   `json:"template"`
	Version     int      `json:"version"`
	UpdatedAt   int64    `json:"updated_at,omitempty"`
	Definitions []string `json:"definitions,omitempty"`
}

func (t NotificationTemplate) Validate() error {
	if strings.TrimSpace(t.Name) == "" || len(t.Name) > 128 || strings.ContainsAny(t.Name, "\r\n\x00") || strings.HasPrefix(t.Name, "__metricspanel_") {
		return errors.New("notification template requires a name of 1–128 bytes")
	}
	if strings.TrimSpace(t.Template) == "" || len(t.Template) > 65536 {
		return errors.New("notification template source must be 1–65536 bytes")
	}
	return nil
}

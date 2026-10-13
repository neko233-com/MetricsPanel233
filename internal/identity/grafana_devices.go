package identity

import (
	"context"
	"database/sql"
	"sync"
	"time"

	"github.com/ua-parser/uap-go/uaparser"
)

type GrafanaAuthToken struct {
	ID             int64     `json:"id"`
	IsActive       bool      `json:"isActive"`
	ClientIP       string    `json:"clientIp"`
	Device         string    `json:"device"`
	OS             string    `json:"os"`
	OSVersion      string    `json:"osVersion"`
	Browser        string    `json:"browser"`
	BrowserVersion string    `json:"browserVersion"`
	AuthModule     string    `json:"authModule"`
	CreatedAt      time.Time `json:"createdAt"`
	SeenAt         time.Time `json:"seenAt"`
}

// The exact parser/database version is the one pinned by Grafana 13.2.3. New()
// loads the same saved regex definitions as its deprecated NewFromSaved().
var grafanaDeviceParser = sync.OnceValues(func() (*uaparser.Parser, error) { return uaparser.New() })
var grafanaDeviceParserMu sync.Mutex

func grafanaDeviceDTO(session Session, activeSessionID int64) (GrafanaAuthToken, error) {
	parser, err := grafanaDeviceParser()
	if err != nil {
		return GrafanaAuthToken{}, err
	}
	// Do not rewrite native descriptive metadata. Bounded input limits both parse
	// cost and externally written legacy headers; parsing never affects access.
	ua := normalizeSessionMetadata(SessionMetadataInput{UserAgent: session.UserAgent}).userAgent
	grafanaDeviceParserMu.Lock()
	client := parser.Parse(ua)
	grafanaDeviceParserMu.Unlock()
	version := func(major, minor string) string {
		if major == "" {
			return ""
		}
		if minor == "" {
			return major
		}
		return major + "." + minor
	}
	seen := session.LastSeen
	if seen == 0 {
		seen = session.CreatedAt
	}
	// Pinned local-password devices have no external-session auth-module label.
	return GrafanaAuthToken{ID: session.ID, IsActive: activeSessionID > 0 && session.ID == activeSessionID,
		ClientIP: session.ClientIP, Device: client.Device.ToString(), OS: client.Os.Family,
		OSVersion: version(client.Os.Major, client.Os.Minor), Browser: client.UserAgent.Family,
		BrowserVersion: version(client.UserAgent.Major, client.UserAgent.Minor),
		CreatedAt:      time.UnixMilli(session.CreatedAt).UTC(), SeenAt: time.UnixMilli(seen).UTC()}, nil
}

func (s *Store) CurrentUserAuthTokens(ctx context.Context, p Principal, activeSessionID int64) ([]GrafanaAuthToken, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := currentAdapterUser(ctx, tx, p); err != nil {
		return nil, err
	}
	now := time.Now().UnixMilli()
	if err := validateAdapterSession(ctx, tx, p.ID, activeSessionID, now); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+sessionColumns+` FROM browser_sessions WHERE user_id=? AND revoked_at=0 AND expires_at>? AND last_seen+idle_timeout_ms>? ORDER BY id DESC`, p.ID, now, now)
	if err != nil {
		return nil, err
	}
	sessions := []Session{}
	for rows.Next() {
		var session Session
		session, err = scanSession(rows)
		if err != nil {
			break
		}
		sessions = append(sessions, session)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	result := make([]GrafanaAuthToken, 0, len(sessions))
	// Keep potentially costly UA parsing outside the catalog transaction.
	for _, session := range sessions {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		item, err := grafanaDeviceDTO(session, activeSessionID)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, nil
}

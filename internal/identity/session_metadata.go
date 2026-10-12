package identity

import (
	"database/sql"
	"net/http"
	"net/netip"
	"strings"
	"unicode"
)

const maxSessionUserAgentBytes = 1024

// These fields are untrusted descriptive data, never an identity/permission
// source. Password login chooses its auth module internally.
type SessionMetadataInput struct {
	RemoteAddr string
	UserAgent  string
}

type sessionMetadata struct{ clientIP, userAgent, authModule string }

// Capture only the actual peer address and User-Agent. No forwarded, cookie or
// authorization header is considered a source of stored device metadata.
func SessionMetadataFromRequest(r *http.Request) SessionMetadataInput {
	if r == nil {
		return SessionMetadataInput{}
	}
	return SessionMetadataInput{RemoteAddr: r.RemoteAddr, UserAgent: r.UserAgent()}
}

func normalizeSessionMetadata(input SessionMetadataInput) sessionMetadata {
	metadata := sessionMetadata{authModule: "password"}
	if peer, err := netip.ParseAddrPort(input.RemoteAddr); err == nil {
		metadata.clientIP = peer.Addr().WithZone("").Unmap().String()
	} else if peer, err := netip.ParseAddr(input.RemoteAddr); err == nil {
		metadata.clientIP = peer.WithZone("").Unmap().String()
	}
	ua := input.UserAgent
	if len(ua) > maxSessionUserAgentBytes {
		ua = ua[:maxSessionUserAgentBytes]
	}
	ua = strings.ToValidUTF8(ua, "")
	ua = strings.Map(func(char rune) rune {
		if unicode.IsControl(char) {
			return -1
		}
		return char
	}, ua)
	metadata.userAgent = ua
	return metadata
}

func initializeSessionMetadata(tx *sql.Tx) error {
	columns, err := metadataTableColumns(tx, "browser_sessions")
	if err != nil {
		return err
	}
	for _, column := range []string{"client_ip", "user_agent", "auth_module"} {
		if !columns[column] {
			if _, err := tx.Exec(`ALTER TABLE browser_sessions ADD COLUMN ` + column + ` TEXT NOT NULL DEFAULT ''`); err != nil {
				return err
			}
		}
	}
	return nil
}

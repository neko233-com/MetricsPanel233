package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/backend/gtime"
	"github.com/grafana/grafana-plugin-sdk-go/data"
	"github.com/lib/pq"
	"github.com/neko233-com/MetricsPanel233/internal/model"
)

const postgresSourceID = "grafana-postgresql-datasource"

// Grafana renamed PostgreSQL while retaining postgres as an import alias.
func sqlSourceKind(kind string) string {
	if kind == postgresSourceID {
		return "postgres"
	}
	return kind
}

func isSQLSource(kind string) bool {
	kind = sqlSourceKind(kind)
	return kind == "mysql" || kind == "postgres"
}

type sqlSourceSettings struct {
	SSLMode           string `json:"sslmode"`
	TLSSkipVerify     bool   `json:"tlsSkipVerify"`
	TLSAuth           bool   `json:"tlsAuth"`
	TLSAuthWithCACert bool   `json:"tlsAuthWithCACert"`
	Database          string `json:"database"`
	QueryTimeout      *int   `json:"queryTimeout"`
	MaxRows           *int   `json:"maxRows"`
	MaxOpenConns      *int   `json:"maxOpenConns"`
	MaxIdleConns      *int   `json:"maxIdleConns"`
	ConnMaxLifetime   *int   `json:"connMaxLifetime"`
	TimescaleDB       bool   `json:"timescaledb"`
}

type sqlSourceConfig struct {
	host, port, user, database, sslmode string
	settings                            sqlSourceSettings
	maxRows, maxOpen, maxIdle           int
	lifetime, timeout                   time.Duration
}

func sqlOption(value *int, fallback, min, max int, name string) (int, error) {
	if value == nil {
		return fallback, nil
	}
	if *value < min || *value > max {
		return 0, fmt.Errorf("%s must be %d–%d", name, min, max)
	}
	return *value, nil
}

func sqlSourceConfiguration(ds model.DataSource) (sqlSourceConfig, error) {
	var config sqlSourceConfig
	ds.Type = sqlSourceKind(ds.Type)
	if !isSQLSource(ds.Type) {
		return config, errors.New("unsupported SQL datasource type")
	}
	if len(ds.JSONData) != 0 && json.Unmarshal(ds.JSONData, &config.settings) != nil {
		return config, errors.New("invalid SQL datasource jsonData")
	}
	address := strings.TrimSpace(ds.URL)
	if address == "" {
		return config, errors.New("SQL datasource host is required")
	}
	if !strings.Contains(address, "://") {
		address = "tcp://" + address
	}
	u, err := url.Parse(address)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return config, errors.New("SQL datasource URL needs a host and port without credentials or query options")
	}
	valid := u.Scheme == "tcp" || ds.Type == "mysql" && (u.Scheme == "mysql" || u.Scheme == "mariadb") || ds.Type == "postgres" && (u.Scheme == "postgres" || u.Scheme == "postgresql")
	if !valid || u.Hostname() == "" || strings.ContainsAny(u.Hostname(), " \t\n\r,;/\\\x00") {
		return config, errors.New("SQL datasource URL has an invalid protocol or host")
	}
	config.host, config.port = u.Hostname(), u.Port()
	if config.port == "" {
		if ds.Type == "mysql" {
			config.port = "3306"
		} else {
			config.port = "5432"
		}
	}
	port, err := strconv.Atoi(config.port)
	if err != nil || port < 1 || port > 65535 {
		return config, errors.New("SQL datasource port must be 1–65535")
	}
	config.user, config.database = ds.User, ds.Database
	if config.database == "" {
		config.database = config.settings.Database
	}
	if config.database == "" {
		config.database = strings.TrimPrefix(u.Path, "/")
	}
	if config.user == "" || config.database == "" || strings.ContainsAny(config.user+config.database, "\x00\r\n") || strings.Contains(config.database, "/") {
		return config, errors.New("SQL datasource user and database are required without control characters")
	}
	config.sslmode = config.settings.SSLMode
	if config.sslmode == "" {
		if ds.Type == "postgres" {
			config.sslmode = "verify-full"
		} else if config.settings.TLSAuth || config.settings.TLSAuthWithCACert {
			config.sslmode = "verify-full"
		} else {
			config.sslmode = "disable"
		}
	}
	if config.settings.TLSSkipVerify && config.sslmode != "disable" {
		config.sslmode = "require"
	}
	if config.sslmode != "disable" && config.sslmode != "require" && config.sslmode != "verify-ca" && config.sslmode != "verify-full" {
		return config, errors.New("SQL sslmode must be disable, require, verify-ca or verify-full")
	}
	if config.sslmode == "disable" && (config.settings.TLSAuth || config.settings.TLSAuthWithCACert) {
		return config, errors.New("TLS authentication requires an encrypted SQL connection")
	}
	seconds, err := sqlOption(config.settings.QueryTimeout, 20, 1, 20, "queryTimeout")
	if err != nil {
		return config, err
	}
	config.timeout = time.Duration(seconds) * time.Second
	config.maxRows, err = sqlOption(config.settings.MaxRows, 10000, 1, 10000, "maxRows")
	if err != nil {
		return config, err
	}
	config.maxOpen, err = sqlOption(config.settings.MaxOpenConns, 2, 1, 10, "maxOpenConns")
	if err != nil {
		return config, err
	}
	config.maxIdle, err = sqlOption(config.settings.MaxIdleConns, min(2, config.maxOpen), 0, config.maxOpen, "maxIdleConns")
	if err != nil {
		return config, err
	}
	seconds, err = sqlOption(config.settings.ConnMaxLifetime, 600, 60, 3600, "connMaxLifetime")
	if err != nil {
		return config, err
	}
	config.lifetime = time.Duration(seconds) * time.Second
	return config, nil
}

type sqlSourcePool struct {
	mu      sync.Mutex
	entries map[string]*sqlSourceDatabase
	closed  bool
}

type sqlSourceDatabase struct {
	db      *sql.DB
	version int
	config  sqlSourceConfig
}

func (s *Server) invalidateSQLSource(uid string) {
	s.sqlSources.mu.Lock()
	defer s.sqlSources.mu.Unlock()
	if source := s.sqlSources.entries[uid]; source != nil {
		_ = source.db.Close()
		delete(s.sqlSources.entries, uid)
	}
}

func (s *Server) sqlDatabase(ctx context.Context, ds model.DataSource) (*sqlSourceDatabase, map[string]string, error) {
	config, err := sqlSourceConfiguration(ds)
	if err != nil {
		return nil, nil, err
	}
	secrets, err := s.Store.DataSourceSecrets(ctx, ds.UID)
	if err != nil {
		return nil, nil, errors.New("SQL datasource credentials could not be read")
	}
	s.sqlSources.mu.Lock()
	defer s.sqlSources.mu.Unlock()
	if s.sqlSources.closed {
		return nil, nil, errors.New("SQL datasource pools are closed")
	}
	if source := s.sqlSources.entries[ds.UID]; source != nil && source.version == ds.Version {
		return source, secrets, nil
	}
	db, err := openSQLDatabase(ds.Type, config, secrets)
	if err != nil {
		return nil, nil, err
	}
	if s.sqlSources.entries == nil {
		s.sqlSources.entries = map[string]*sqlSourceDatabase{}
	}
	if old := s.sqlSources.entries[ds.UID]; old != nil {
		_ = old.db.Close()
	}
	source := &sqlSourceDatabase{db: db, version: ds.Version, config: config}
	s.sqlSources.entries[ds.UID] = source
	return source, secrets, nil
}

func openSQLDatabase(kind string, config sqlSourceConfig, secrets map[string]string) (*sql.DB, error) {
	var db *sql.DB
	kind = sqlSourceKind(kind)
	if kind == "mysql" {
		settings := mysql.NewConfig()
		settings.Net, settings.Addr = "tcp", net.JoinHostPort(config.host, config.port)
		settings.User, settings.Passwd, settings.DBName = config.user, secrets["password"], config.database
		settings.Timeout, settings.ReadTimeout, settings.WriteTimeout = config.timeout, config.timeout, config.timeout
		settings.ParseTime, settings.Loc, settings.MultiStatements = true, time.UTC, false
		settings.MaxAllowedPacket = 8 << 20
		settings.Params = map[string]string{"time_zone": "'+00:00'"}
		if config.sslmode != "disable" {
			tlsConfig, err := sqlTLSConfig(config, secrets)
			if err != nil {
				return nil, err
			}
			settings.TLS = tlsConfig
		}
		connector, err := mysql.NewConnector(settings)
		if err != nil {
			return nil, errors.New("invalid MySQL connection settings")
		}
		db = sql.OpenDB(connector)
	} else {
		port, _ := strconv.ParseUint(config.port, 10, 16)
		settings := pq.Config{Host: config.host, Port: uint16(port), User: config.user, Password: secrets["password"], Database: config.database,
			SSLMode: pq.SSLMode(config.sslmode), SSLInline: true, SSLSNI: true, ConnectTimeout: config.timeout, ApplicationName: "MetricsPanel233"}
		settings.Runtime = map[string]string{"timezone": "UTC", "statement_timeout": strconv.FormatInt(config.timeout.Milliseconds(), 10)}
		if config.sslmode != "disable" {
			if _, err := sqlTLSConfig(config, secrets); err != nil {
				return nil, err
			}
			settings.SSLRootCert = secrets["tlsCACert"]
			if config.settings.TLSAuth {
				settings.SSLCert, settings.SSLKey = secrets["tlsClientCert"], secrets["tlsClientKey"]
			}
		}
		connector, err := pq.NewConnectorConfig(settings)
		if err != nil {
			return nil, errors.New("invalid PostgreSQL connection settings")
		}
		db = sql.OpenDB(connector)
	}
	db.SetMaxOpenConns(config.maxOpen)
	db.SetMaxIdleConns(config.maxIdle)
	db.SetConnMaxLifetime(config.lifetime)
	db.SetConnMaxIdleTime(min(config.lifetime, 5*time.Minute))
	return db, nil
}

func sqlTLSConfig(config sqlSourceConfig, secrets map[string]string) (*tls.Config, error) {
	settings := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: config.host}
	if config.sslmode == "require" {
		settings.InsecureSkipVerify = true
	}
	if ca := secrets["tlsCACert"]; ca != "" {
		roots, err := x509.SystemCertPool()
		if err != nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM([]byte(ca)) {
			return nil, errors.New("SQL TLS CA certificate is not valid PEM")
		}
		settings.RootCAs = roots
	} else if config.settings.TLSAuthWithCACert {
		return nil, errors.New("SQL TLS CA certificate is required")
	}
	if config.settings.TLSAuth {
		cert, err := tls.X509KeyPair([]byte(secrets["tlsClientCert"]), []byte(secrets["tlsClientKey"]))
		if err != nil {
			return nil, errors.New("SQL TLS client certificate and key must be a valid matching PEM pair")
		}
		settings.Certificates = []tls.Certificate{cert}
	}
	if config.sslmode == "verify-ca" {
		settings.InsecureSkipVerify = true
		settings.VerifyConnection = func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return errors.New("SQL TLS server did not return a certificate")
			}
			intermediates := x509.NewCertPool()
			for _, certificate := range state.PeerCertificates[1:] {
				intermediates.AddCert(certificate)
			}
			_, err := state.PeerCertificates[0].Verify(x509.VerifyOptions{Roots: settings.RootCAs, Intermediates: intermediates, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
			return err
		}
	}
	return settings, nil
}

func safeSQLError(err error, secrets map[string]string) error {
	if err == nil {
		return nil
	}
	text := err.Error()
	for _, secret := range secrets {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, "[redacted]")
		}
	}
	if len(text) > 4096 {
		text = text[:4096] + "…"
	}
	return errors.New(text)
}

func builtinSQLPluginMetadata(kind string) map[string]any {
	name := "MySQL"
	if sqlSourceKind(kind) == "postgres" {
		name = "PostgreSQL"
		kind = postgresSourceID
	}
	metadata := map[string]any{"id": kind, "name": name, "type": "datasource", "builtIn": true, "backend": true, "enabled": true,
		"metrics": true, "annotations": true, "alerting": true, "signature": "internal", "signatureType": "grafana", "signatureOrg": "",
		"module": "builtin:" + kind, "baseUrl": "", "preload": false, "pinned": false, "autoEnabled": true,
		"info":          map[string]any{"version": "13.2.3", "description": "SQL database queries", "logos": map[string]string{"small": "", "large": ""}},
		"latestVersion": "", "hasUpdate": false, "angularDetected": false, "defaultNavUrl": "/connections/datasources"}
	if kind == postgresSourceID {
		metadata["aliasIDs"] = []string{"postgres"}
	}
	return metadata
}

func (s *Server) checkSQLSource(ctx context.Context, ds model.DataSource) error {
	source, secrets, err := s.sqlDatabase(ctx, ds)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, source.config.timeout)
	defer cancel()
	var result int
	if err := source.db.QueryRowContext(ctx, "SELECT 1").Scan(&result); err != nil {
		return safeSQLError(fmt.Errorf("SQL connection check failed: %w", err), secrets)
	}
	if result != 1 {
		return errors.New("SQL connection check returned an unexpected result")
	}
	return nil
}

type sqlSourceQuery struct {
	RawSQL string `json:"rawSql"`
	Format string `json:"format"`
}

type sqlMacroContext struct {
	kind         string
	query        backend.DataQuery
	timescale    bool
	fill         *data.FillMissing
	fillInterval time.Duration
}

func (s *Server) querySQLSource(ctx context.Context, ds model.DataSource, query backend.DataQuery) (data.Frames, error) {
	var input sqlSourceQuery
	if json.Unmarshal(query.JSON, &input) != nil || len(strings.TrimSpace(input.RawSQL)) == 0 || len(input.RawSQL) > 128<<10 || strings.ContainsRune(input.RawSQL, '\x00') {
		return nil, errors.New("SQL query rawSql must be nonempty and at most 128 KiB")
	}
	if input.Format == "" {
		input.Format = "time_series"
	}
	if input.Format != "table" && input.Format != "time_series" {
		return nil, errors.New("SQL query format must be table or time_series")
	}
	source, secrets, err := s.sqlDatabase(ctx, ds)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, source.config.timeout)
	defer cancel()
	macros := sqlMacroContext{kind: sqlSourceKind(ds.Type), query: query, timescale: source.config.settings.TimescaleDB}
	statement, err := macros.expand(input.RawSQL)
	if err != nil {
		return nil, err
	}
	transaction, err := source.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, safeSQLError(err, secrets)
	}
	defer transaction.Rollback()
	rows, err := transaction.QueryContext(ctx, statement)
	if err != nil {
		return nil, safeSQLError(err, secrets)
	}
	defer rows.Close()
	frame, err := sqlRowsFrame(ctx, rows, query.RefID, source.config.maxRows)
	if err != nil {
		return nil, safeSQLError(err, secrets)
	}
	if input.Format == "table" {
		frame.Meta = &data.FrameMeta{Type: data.FrameTypeTable, TypeVersion: data.FrameTypeVersion{0, 1}, PreferredVisualization: data.VisTypeTable}
		return data.Frames{frame}, nil
	}
	frame, err = sqlTimeSeries(ctx, frame, macros.fill, macros.fillInterval, source.config.maxRows)
	if err != nil {
		return nil, safeSQLError(err, secrets)
	}
	frame.RefID = query.RefID
	return data.Frames{frame}, nil
}

// Find the end of a quoted SQL token; macro expansion never alters literals.
func sqlQuotedEnd(text string, start int, backslash bool) (int, error) {
	quote := text[start]
	for i := start + 1; i < len(text); i++ {
		if backslash && text[i] == '\\' {
			i++
			continue
		}
		if text[i] == quote {
			if i+1 < len(text) && text[i+1] == quote {
				i++
				continue
			}
			return i + 1, nil
		}
	}
	return 0, errors.New("SQL query has an unterminated quoted value")
}

func sqlProtectedEnd(text string, start int, kind string) (int, bool, error) {
	if text[start] == '\'' || text[start] == '"' || text[start] == '`' {
		backslash := kind == "mysql" || text[start] == '\'' && start > 0 && (text[start-1] == 'E' || text[start-1] == 'e')
		end, err := sqlQuotedEnd(text, start, backslash)
		return end, true, err
	}
	if strings.HasPrefix(text[start:], "--") || kind == "mysql" && text[start] == '#' {
		end := strings.IndexByte(text[start:], '\n')
		if end < 0 {
			return len(text), true, nil
		}
		return start + end + 1, true, nil
	}
	if strings.HasPrefix(text[start:], "/*") {
		depth := 1
		for i := start + 2; i < len(text)-1; i++ {
			if kind == "postgres" && strings.HasPrefix(text[i:], "/*") {
				depth++
				i++
				continue
			}
			if strings.HasPrefix(text[i:], "*/") {
				depth--
				if depth == 0 {
					return i + 2, true, nil
				}
				i++
			}
		}
		return 0, true, errors.New("SQL query has an unterminated comment")
	}
	if kind == "postgres" && text[start] == '$' {
		end := start + 1
		for end < len(text) && sqlMacroIdentifier(text[end]) {
			end++
		}
		if end < len(text) && text[end] == '$' {
			delimiter := text[start : end+1]
			finish := strings.Index(text[end+1:], delimiter)
			if finish < 0 {
				return 0, true, errors.New("SQL query has an unterminated dollar-quoted value")
			}
			return finish + end + 1 + len(delimiter), true, nil
		}
	}
	return start, false, nil
}

func sqlMacroIdentifier(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9' || value == '_'
}

func sqlMacroArguments(text string, start int, kind string) ([]string, int, error) {
	depth, segment := 1, start+1
	args := []string{}
	for i := start + 1; i < len(text); i++ {
		if end, protected, err := sqlProtectedEnd(text, i, kind); err != nil {
			return nil, 0, err
		} else if protected {
			i = end - 1
			continue
		}
		switch text[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				value := strings.TrimSpace(text[segment:i])
				if value != "" || len(args) > 0 {
					args = append(args, value)
				}
				return args, i + 1, nil
			}
		case ',':
			if depth == 1 {
				args = append(args, strings.TrimSpace(text[segment:i]))
				segment = i + 1
			}
		}
	}
	return nil, 0, errors.New("SQL macro has an unterminated argument list")
}

func (m *sqlMacroContext) expand(statement string) (string, error) {
	var output strings.Builder
	for i := 0; i < len(statement); {
		if end, protected, err := sqlProtectedEnd(statement, i, m.kind); err != nil {
			return "", err
		} else if protected {
			output.WriteString(statement[i:end])
			i = end
			continue
		}
		if !strings.HasPrefix(statement[i:], "$__") {
			output.WriteByte(statement[i])
			i++
			continue
		}
		end := i + 3
		for end < len(statement) && sqlMacroIdentifier(statement[end]) {
			end++
		}
		name := statement[i+1 : end]
		var args []string
		if end < len(statement) && statement[end] == '(' {
			var err error
			args, end, err = sqlMacroArguments(statement, end, m.kind)
			if err != nil {
				return "", err
			}
		}
		// Older PostgreSQL dashboards use timeGroup as a SELECT column
		// immediately before a comma and expect Grafana's implicit time alias.
		if m.kind == "postgres" && name == "__timeGroup" && end < len(statement) && statement[end] == ',' {
			name = "__timeGroupAlias"
		}
		value, err := m.evaluate(name, args)
		if err != nil {
			return "", err
		}
		output.WriteString(value)
		i = end
	}
	return output.String(), nil
}

func (m *sqlMacroContext) evaluate(name string, args []string) (string, error) {
	from, to := m.query.TimeRange.From.UTC(), m.query.TimeRange.To.UTC()
	column := func() (string, error) {
		if len(args) < 1 || args[0] == "" {
			return "", fmt.Errorf("SQL macro %s needs a column", name)
		}
		return args[0], nil
	}
	stamp := func(value time.Time) string {
		if m.kind == "mysql" {
			if value.Unix() < 0 {
				return fmt.Sprintf("DATE_ADD(FROM_UNIXTIME(0), INTERVAL %d SECOND)", value.Unix())
			}
			return fmt.Sprintf("FROM_UNIXTIME(%d)", value.Unix())
		}
		return "'" + value.Format(time.RFC3339Nano) + "'"
	}
	switch name {
	case "__from":
		return strconv.FormatInt(from.UnixMilli(), 10), nil
	case "__to":
		return strconv.FormatInt(to.UnixMilli(), 10), nil
	case "__interval":
		interval := m.query.Interval
		if interval <= 0 {
			interval = 15 * time.Second
		}
		return "'" + interval.String() + "'", nil
	case "__interval_ms":
		return strconv.FormatInt(max(m.query.Interval.Milliseconds(), 1), 10), nil
	case "__timeFrom":
		return stamp(from), nil
	case "__timeTo":
		return stamp(to), nil
	case "__unixEpochFrom":
		return strconv.FormatInt(from.Unix(), 10), nil
	case "__unixEpochTo":
		return strconv.FormatInt(to.Unix(), 10), nil
	case "__unixEpochNanoFrom", "__unixEpochNanoTo", "__unixEpochNanoFilter":
		if from.Unix() < 0 || to.Unix() > math.MaxInt64/int64(time.Second) {
			return "", errors.New("SQL nanosecond macro time range is outside supported bounds")
		}
		if name == "__unixEpochNanoFrom" {
			return strconv.FormatInt(from.UnixNano(), 10), nil
		}
		if name == "__unixEpochNanoTo" {
			return strconv.FormatInt(to.UnixNano(), 10), nil
		}
		col, err := column()
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%s >= %d AND %s <= %d", col, from.UnixNano(), col, to.UnixNano()), nil
	case "__time", "__timeEpoch":
		col, err := column()
		if err != nil {
			return "", err
		}
		if m.kind == "mysql" {
			return fmt.Sprintf("UNIX_TIMESTAMP(%s) AS time_sec", col), nil
		}
		if name == "__timeEpoch" {
			return fmt.Sprintf("extract(epoch from %s) AS \"time\"", col), nil
		}
		return col + " AS \"time\"", nil
	case "__timeFilter":
		col, err := column()
		if err != nil {
			return "", err
		}
		return col + " BETWEEN " + stamp(from) + " AND " + stamp(to), nil
	case "__unixEpochFilter":
		col, err := column()
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%s >= %d AND %s <= %d", col, from.Unix(), col, to.Unix()), nil
	case "__timeGroup", "__timeGroupAlias", "__unixEpochGroup", "__unixEpochGroupAlias":
		col, err := column()
		if err != nil {
			return "", err
		}
		if len(args) < 2 || len(args) > 3 {
			return "", fmt.Errorf("SQL macro %s needs a column, interval and optional fill value", name)
		}
		value := strings.Trim(args[1], "'\"")
		if value == "$__interval" {
			value = m.query.Interval.String()
			if m.query.Interval <= 0 {
				value = "15s"
			}
		}
		interval, err := gtime.ParseInterval(value)
		if err != nil || interval < time.Millisecond {
			return "", errors.New("SQL grouping interval must be a valid interval of at least 1 ms")
		}
		if len(args) == 3 {
			fill := strings.Trim(args[2], "'\"")
			if fill == "null" || fill == "NULL" {
				m.fill = &data.FillMissing{Mode: data.FillModeNull}
			} else if fill == "previous" {
				m.fill = &data.FillMissing{Mode: data.FillModePrevious}
			} else {
				number, err := strconv.ParseFloat(fill, 64)
				if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
					return "", errors.New("SQL group fill must be null, previous or a finite number")
				}
				m.fill = &data.FillMissing{Mode: data.FillModeValue, Value: number}
			}
			m.fillInterval = interval
		}
		seconds := strconv.FormatFloat(interval.Seconds(), 'f', -1, 64)
		var expression string
		if strings.HasPrefix(name, "__unixEpoch") {
			expression = fmt.Sprintf("floor((%s)/%s)*%s", col, seconds, seconds)
		} else if m.kind == "mysql" {
			expression = fmt.Sprintf("floor(UNIX_TIMESTAMP(%s)/%s)*%s", col, seconds, seconds)
		} else if m.timescale {
			expression = fmt.Sprintf("time_bucket('%d milliseconds',%s)", interval.Milliseconds(), col)
		} else {
			expression = fmt.Sprintf("floor(extract(epoch from %s)/%s)*%s", col, seconds, seconds)
		}
		if strings.HasSuffix(name, "Alias") {
			expression += " AS \"time\""
		}
		return expression, nil
	default:
		return "", fmt.Errorf("unsupported SQL macro %s", name)
	}
}

func sqlColumnType(column *sql.ColumnType) data.FieldType {
	name := strings.ToUpper(column.DatabaseTypeName())
	unsigned := strings.Contains(name, "UNSIGNED")
	name = strings.TrimSpace(strings.ReplaceAll(name, "UNSIGNED", ""))
	switch name {
	case "TINYINT", "SMALLINT", "MEDIUMINT", "INT", "INTEGER", "BIGINT", "INT2", "INT4", "INT8", "SERIAL", "BIGSERIAL", "SMALLSERIAL", "YEAR":
		if unsigned {
			return data.FieldTypeNullableUint64
		}
		return data.FieldTypeNullableInt64
	case "DECIMAL", "NEWDECIMAL", "NUMERIC", "FLOAT", "DOUBLE", "REAL", "FLOAT4", "FLOAT8", "MONEY":
		return data.FieldTypeNullableFloat64
	case "BOOL", "BOOLEAN":
		return data.FieldTypeNullableBool
	case "TIMESTAMP", "TIMESTAMPTZ", "TIMESTAMP WITH TIME ZONE", "TIMESTAMP WITHOUT TIME ZONE", "DATETIME", "DATE":
		return data.FieldTypeNullableTime
	default:
		return data.FieldTypeNullableString
	}
}

func sqlCellText(value any) string {
	switch value := value.(type) {
	case []byte:
		return string(value)
	case string:
		return value
	default:
		return fmt.Sprint(value)
	}
}

func sqlCellValue(value any, kind data.FieldType, typeName string) (any, int, error) {
	if value == nil {
		switch kind {
		case data.FieldTypeNullableInt64:
			return (*int64)(nil), 8, nil
		case data.FieldTypeNullableUint64:
			return (*uint64)(nil), 8, nil
		case data.FieldTypeNullableFloat64:
			return (*float64)(nil), 8, nil
		case data.FieldTypeNullableBool:
			return (*bool)(nil), 1, nil
		case data.FieldTypeNullableTime:
			return (*time.Time)(nil), 24, nil
		default:
			return (*string)(nil), 8, nil
		}
	}
	text := sqlCellText(value)
	if len(text) > 1<<20 {
		return nil, 0, errors.New("SQL result cell exceeds 1 MiB")
	}
	switch kind {
	case data.FieldTypeNullableInt64:
		number, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return nil, 0, errors.New("SQL integer could not be converted")
		}
		return &number, 8, nil
	case data.FieldTypeNullableUint64:
		number, err := strconv.ParseUint(text, 10, 64)
		if err != nil {
			return nil, 0, errors.New("SQL unsigned integer could not be converted")
		}
		return &number, 8, nil
	case data.FieldTypeNullableFloat64:
		number, err := strconv.ParseFloat(text, 64)
		if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
			return nil, 0, errors.New("SQL numeric result must be finite")
		}
		return &number, 8, nil
	case data.FieldTypeNullableBool:
		boolean, err := strconv.ParseBool(text)
		if err != nil {
			return nil, 0, errors.New("SQL boolean could not be converted")
		}
		return &boolean, 1, nil
	case data.FieldTypeNullableTime:
		if instant, ok := value.(time.Time); ok {
			instant = instant.UTC()
			return &instant, 24, nil
		}
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999Z07:00", "2006-01-02 15:04:05.999999999-07", "2006-01-02 15:04:05.999999999", "2006-01-02"} {
			instant, err := time.ParseInLocation(layout, text, time.UTC)
			if err == nil {
				return &instant, 24, nil
			}
		}
		return nil, 0, errors.New("SQL date/time could not be converted")
	default:
		upper := strings.ToUpper(typeName)
		if bytes, ok := value.([]byte); ok && (upper == "BYTEA" || strings.Contains(upper, "BLOB") || strings.Contains(upper, "BINARY")) {
			text = base64.StdEncoding.EncodeToString(bytes)
		}
		return &text, len(text) + 16, nil
	}
}

func sqlRowsFrame(ctx context.Context, rows *sql.Rows, ref string, maxRows int) (*data.Frame, error) {
	columns, err := rows.ColumnTypes()
	if err != nil {
		return nil, err
	}
	if len(columns) == 0 || len(columns) > 128 {
		return nil, errors.New("SQL result needs 1–128 columns")
	}
	frame := data.NewFrame(ref)
	frame.RefID = ref
	values := make([]any, len(columns))
	destinations := make([]any, len(columns))
	for i, column := range columns {
		field := data.NewFieldFromFieldType(sqlColumnType(column), 0)
		field.Name = column.Name()
		frame.Fields = append(frame.Fields, field)
		destinations[i] = &values[i]
	}
	bytes := 0
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if frame.Rows() >= maxRows {
			return nil, fmt.Errorf("SQL result exceeds configured limit of %d rows", maxRows)
		}
		if frame.Rows() >= 200000/len(columns) {
			return nil, errors.New("SQL result exceeds 200000 cells")
		}
		if err := rows.Scan(destinations...); err != nil {
			return nil, err
		}
		for i, value := range values {
			converted, size, err := sqlCellValue(value, frame.Fields[i].Type(), columns[i].DatabaseTypeName())
			if err != nil {
				return nil, fmt.Errorf("SQL column %d: %w", i+1, err)
			}
			bytes += size
			if bytes > 16<<20 {
				return nil, errors.New("SQL result exceeds 16 MiB")
			}
			frame.Fields[i].Append(converted)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if rows.NextResultSet() {
		return nil, errors.New("SQL query must return a single result set")
	}
	return frame, nil
}

func sqlTimeSeries(ctx context.Context, frame *data.Frame, fill *data.FillMissing, interval time.Duration, maxRows int) (*data.Frame, error) {
	timeIndex, numeric := -1, 0
	for i, field := range frame.Fields {
		if strings.EqualFold(field.Name, "time") || strings.EqualFold(field.Name, "time_sec") {
			timeIndex = i
		}
		if field.Type().Numeric() {
			numeric++
		}
	}
	if timeIndex < 0 {
		return nil, errors.New("SQL time_series needs a time or time_sec column")
	}
	clock := frame.Fields[timeIndex]
	if clock.Type().Numeric() {
		numeric--
	}
	if numeric == 0 {
		return nil, errors.New("SQL time_series needs a numeric value column")
	}
	instants := make([]time.Time, clock.Len())
	for i := 0; i < clock.Len(); i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if value, ok := clock.ConcreteAt(i); !ok {
			return nil, errors.New("SQL time_series timestamps must not be null")
		} else if instant, ok := value.(time.Time); ok {
			instants[i] = instant.UTC()
		} else {
			seconds, err := clock.FloatAt(i)
			if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 || seconds > 1e11 {
				return nil, errors.New("SQL time_series time must be a datetime or Unix seconds")
			}
			whole, fraction := math.Modf(seconds)
			instants[i] = time.Unix(int64(whole), int64(math.Round(fraction*1e9))).UTC()
		}
		if i > 0 && instants[i].Before(instants[i-1]) {
			return nil, errors.New("SQL time_series must be sorted by time ascending")
		}
	}
	frame.Fields[timeIndex] = data.NewField("time", nil, instants)
	if timeIndex != 0 {
		field := frame.Fields[timeIndex]
		copy(frame.Fields[1:timeIndex+1], frame.Fields[:timeIndex])
		frame.Fields[0] = field
	}
	for _, field := range frame.Fields[1:] {
		kind := field.Type()
		if !kind.Numeric() && kind != data.FieldTypeString && kind != data.FieldTypeNullableString && kind != data.FieldTypeBool && kind != data.FieldTypeNullableBool {
			return nil, errors.New("SQL time_series value columns must be numeric; dimensions must be strings or booleans")
		}
	}
	legacyMetric := len(frame.Fields) == 3 && numeric == 1 && strings.EqualFold(frame.Fields[2].Name, "metric") && (frame.Fields[2].Type() == data.FieldTypeNullableString || frame.Fields[2].Type() == data.FieldTypeString)
	for _, field := range frame.Fields {
		if strings.EqualFold(field.Name, "metric") && len(frame.Fields) == 3 && numeric == 1 && (field.Type() == data.FieldTypeString || field.Type() == data.FieldTypeNullableString) {
			legacyMetric = true
		}
	}
	schema := frame.TimeSeriesSchema()
	if frame.Rows() == 0 {
		fields := []*data.Field{frame.Fields[0]}
		for _, field := range frame.Fields[1:] {
			if field.Type().Numeric() {
				fields = append(fields, field)
			}
		}
		frame.Fields = fields
	} else if schema.Type == data.TimeSeriesTypeLong {
		// A sparse long query can otherwise expand into a much larger dense
		// matrix. Bound that matrix before the SDK allocates it.
		dimensions := map[string]bool{}
		timestamps := 0
		for row, instant := range instants {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if row == 0 || !instant.Equal(instants[row-1]) {
				timestamps++
			}
			var key strings.Builder
			for _, index := range schema.FactorIndices {
				value, ok := frame.Fields[index].ConcreteAt(row)
				if !ok {
					value = ""
				}
				key.WriteString(strconv.Quote(fmt.Sprint(value)))
				key.WriteByte(',')
			}
			dimensions[key.String()] = true
		}
		width := 1 + len(dimensions)*len(schema.ValueIndices)
		if width > 10001 || timestamps > 200000/width {
			return nil, errors.New("SQL time series exceeds 200000 cells")
		}
		wide, err := data.LongToWide(frame, &data.FillMissing{Mode: data.FillModeNull})
		if err != nil {
			return nil, fmt.Errorf("SQL time series conversion failed: %w", err)
		}
		frame = wide
	} else if schema.Type != data.TimeSeriesTypeWide {
		return nil, errors.New("SQL result is not a supported time series")
	} else {
		for i := 1; i < len(instants); i++ {
			if instants[i].Equal(instants[i-1]) {
				return nil, errors.New("SQL time_series without dimensions needs unique timestamps")
			}
		}
	}
	if len(frame.Fields) > 10001 || frame.Rows() > 0 && len(frame.Fields) > 200000/frame.Rows() {
		return nil, errors.New("SQL time series exceeds 200000 cells")
	}
	frame.Meta = &data.FrameMeta{Type: data.FrameTypeTimeSeriesWide, TypeVersion: data.FrameTypeVersion{0, 1}}
	if legacyMetric {
		for _, field := range frame.Fields {
			if value, ok := field.Labels["metric"]; ok {
				field.Name = value
				delete(field.Labels, "metric")
			}
		}
	}
	if fill != nil && interval > 0 && frame.Rows() > 0 {
		return fillSQLSeries(ctx, frame, fill, interval, maxRows)
	}
	return frame, nil
}

// Fill only gaps between actual returned buckets. Empty or out-of-range results
// remain empty rather than manufacturing measurements at the request boundaries.
func fillSQLSeries(ctx context.Context, frame *data.Frame, fill *data.FillMissing, interval time.Duration, maxRows int) (*data.Frame, error) {
	clock := frame.Fields[0]
	first, last := clock.At(0).(time.Time), clock.At(clock.Len()-1).(time.Time)
	count := last.Sub(first)/interval + 1
	if count > time.Duration(maxRows) || count > time.Duration(200000/len(frame.Fields)) {
		return nil, errors.New("SQL time-group fill exceeds the result limit")
	}
	result := data.NewFrame(frame.Name)
	result.RefID = frame.RefID
	result.Meta = frame.Meta
	for _, field := range frame.Fields {
		copy := data.NewFieldFromFieldType(field.Type(), 0)
		copy.Name, copy.Labels, copy.Config = field.Name, field.Labels, field.Config
		result.Fields = append(result.Fields, copy)
	}
	index, previous := 0, -1
	for instant := first; !instant.After(last); instant = instant.Add(interval) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for index < clock.Len() && clock.At(index).(time.Time).Before(instant) {
			previous = index
			index++
		}
		if index < clock.Len() && clock.At(index).(time.Time).Equal(instant) {
			for i, field := range frame.Fields {
				result.Fields[i].Append(field.At(index))
			}
			previous = index
			index++
			continue
		}
		result.Fields[0].Append(instant)
		for i := 1; i < len(frame.Fields); i++ {
			missing, err := data.GetMissing(fill, frame.Fields[i], previous)
			if err != nil {
				return nil, err
			}
			result.Fields[i].Append(missing)
		}
	}
	return result, nil
}

// CloseSQLSources releases datasource connection pools, including on shutdown.
func (s *Server) CloseSQLSources() {
	s.sqlSources.mu.Lock()
	defer s.sqlSources.mu.Unlock()
	s.sqlSources.closed = true
	for uid, source := range s.sqlSources.entries {
		_ = source.db.Close()
		delete(s.sqlSources.entries, uid)
	}
}

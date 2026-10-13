package receivers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	OpportunisticStartTLS = "OpportunisticStartTLS"
	MandatoryStartTLS     = "MandatoryStartTLS"
	NoStartTLS            = "NoStartTLS"
)

// EmailSettings uses Grafana email v1 property names. Subject and Message are
// template source; this transport only consumes explicitly rendered Content.
type EmailSettings struct {
	Addresses   string `json:"addresses"`
	SingleEmail bool   `json:"singleEmail,omitempty"`
	Subject     string `json:"subject,omitempty"`
	Message     string `json:"message,omitempty"`
}
type SMTPSettings struct {
	Host           string            `json:"host"`
	AuthUser       string            `json:"auth_user,omitempty"`
	AuthMode       string            `json:"auth_mode,omitempty"` // auto, plain, login, cram-md5, none
	FromAddress    string            `json:"from_address"`
	FromName       string            `json:"from_name,omitempty"`
	EhloIdentity   string            `json:"ehlo_identity,omitempty"`
	StartTLSPolicy string            `json:"starttls_policy,omitempty"`
	ImplicitTLS    bool              `json:"implicit_tls,omitempty"` // also enabled for port 465
	UseBCC         bool              `json:"use_bcc,omitempty"`
	StaticHeaders  map[string]string `json:"static_headers,omitempty"`
	Timeout        time.Duration     `json:"-"`
	TLS            TLSSettings       `json:"tls,omitempty"`
}
type SMTPSecrets struct {
	Password string `json:"-"`
}

func (SMTPSecrets) String() string { return "SMTP secrets" }

type EmailContent struct {
	Subject     string
	TextBody    string
	HTMLBody    string
	ReplyTo     []string
	Attachments []EmailAttachment
}

// EmailRecipients exposes the validated envelope destinations before content
// rendering, so grouped recovery membership can be selected per destination.
func EmailRecipients(settings EmailSettings) ([]string, error) {
	addresses, err := emailAddresses(settings)
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(addresses))
	for _, address := range addresses {
		result = append(result, address.Address)
	}
	return result, nil
}

type EmailAttachment struct {
	Name        string
	ContentType string
	ContentID   string // nonempty means MIME inline; referenced by cid: in HTML
	Data        []byte
}

type SMTP struct {
	settings SMTPSettings
	secrets  SMTPSecrets
	host     string
	from     mail.Address
	tls      *tls.Config
}

var _ Transport = (*SMTP)(nil)
var _ JournaledTransport = (*SMTP)(nil)

func safeLine(value string, limit int) bool {
	return len(value) <= limit && !strings.ContainsAny(value, "\r\n\x00")
}
func envelopeAddress(value string) (*mail.Address, error) {
	if !safeLine(value, 512) {
		return nil, ErrInvalidConfiguration
	}
	a, err := mail.ParseAddress(value)
	if err != nil {
		return nil, ErrInvalidConfiguration
	}
	// This transport currently limits envelopes to ASCII pending explicit
	// SMTPUTF8 capability and MIME tests. Display names may still use UTF-8.
	for _, r := range a.Address {
		if r > 127 || r < 33 {
			return nil, ErrInvalidConfiguration
		}
	}
	if !strings.Contains(a.Address, "@") {
		return nil, ErrInvalidConfiguration
	}
	return a, nil
}
func NewSMTP(settings SMTPSettings, secrets SMTPSecrets) (*SMTP, error) {
	host, port, err := net.SplitHostPort(settings.Host)
	if err != nil || host == "" || !safeLine(host, 253) {
		return nil, ErrInvalidConfiguration
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return nil, ErrInvalidConfiguration
	}
	from, err := envelopeAddress(settings.FromAddress)
	if err != nil {
		return nil, err
	}
	if !safeLine(settings.FromName, 256) || !safeLine(settings.AuthUser, 512) || len(secrets.Password) > 16384 || strings.ContainsRune(secrets.Password, 0) {
		return nil, ErrInvalidConfiguration
	}
	if settings.FromName != "" {
		from.Name = settings.FromName
	}
	if settings.EhloIdentity == "" {
		settings.EhloIdentity = "localhost"
	}
	if settings.EhloIdentity == "" || !safeLine(settings.EhloIdentity, 253) || strings.ContainsAny(settings.EhloIdentity, " \t") {
		return nil, ErrInvalidConfiguration
	}
	if settings.StartTLSPolicy == "" {
		settings.StartTLSPolicy = OpportunisticStartTLS
	}
	switch settings.StartTLSPolicy {
	case OpportunisticStartTLS, MandatoryStartTLS, NoStartTLS:
	default:
		return nil, ErrInvalidConfiguration
	}
	if settings.AuthMode == "" {
		settings.AuthMode = "auto"
	}
	switch settings.AuthMode {
	case "auto", "plain", "login", "cram-md5", "none":
	default:
		return nil, ErrInvalidConfiguration
	}
	if settings.AuthUser == "" && settings.AuthMode != "auto" && settings.AuthMode != "none" {
		return nil, ErrInvalidConfiguration
	}
	if settings.AuthMode == "none" && (settings.AuthUser != "" || secrets.Password != "") {
		return nil, ErrInvalidConfiguration
	}
	if settings.AuthUser == "" && secrets.Password != "" {
		return nil, ErrInvalidConfiguration
	}
	settings.Timeout, err = validateTimeout(settings.Timeout)
	if err != nil {
		return nil, err
	}
	settings.ImplicitTLS = settings.ImplicitTLS || number == 465
	config, err := tlsConfiguration(settings.TLS)
	if err != nil {
		return nil, err
	}
	if config.ServerName == "" {
		config.ServerName = host
	}
	if len(settings.StaticHeaders) > 64 {
		return nil, ErrInvalidConfiguration
	}
	headers := make(map[string]string, len(settings.StaticHeaders))
	for name, value := range settings.StaticHeaders {
		canonical := textproto.CanonicalMIMEHeaderKey(name)
		if !headerName(name) || !safeLine(value, 4096) {
			return nil, ErrInvalidConfiguration
		}
		switch strings.ToLower(canonical) {
		case "from", "to", "bcc", "cc", "subject", "date", "message-id", "mime-version", "content-type", "content-transfer-encoding":
			return nil, ErrInvalidConfiguration
		}
		if _, exists := headers[canonical]; exists {
			return nil, ErrInvalidConfiguration
		}
		headers[canonical] = value
	}
	settings.StaticHeaders = headers
	return &SMTP{settings: settings, secrets: secrets, host: host, from: *from, tls: config}, nil
}
func headerName(name string) bool {
	if len(name) == 0 || len(name) > 128 {
		return false
	}
	for _, r := range name {
		if r <= 32 || r >= 127 || r == ':' {
			return false
		}
	}
	return true
}
func emailAddresses(settings EmailSettings) ([]mail.Address, error) {
	if len(settings.Addresses) > 16384 {
		return nil, ErrInvalidConfiguration
	}
	parts := strings.FieldsFunc(settings.Addresses, func(r rune) bool { return r == ',' || r == ';' || r == '\n' })
	if len(parts) == 0 || len(parts) > MaxOperations {
		return nil, ErrInvalidConfiguration
	}
	addresses := []mail.Address{}
	seen := map[string]bool{}
	for _, part := range parts {
		a, err := envelopeAddress(strings.TrimSpace(part))
		if err != nil {
			return nil, err
		}
		local, domain, _ := strings.Cut(a.Address, "@")
		key := local + "@" + strings.ToLower(domain)
		if seen[key] {
			continue
		}
		seen[key] = true
		addresses = append(addresses, *a)
	}
	return addresses, nil
}
func recipientKey(address string) string {
	sum := sha256.Sum256([]byte(address))
	return "recipient:" + hex.EncodeToString(sum[:])
}

var smtpMessageID = regexp.MustCompile(`^<[a-f0-9]{64}@metricspanel233\.local>$`)

func validateMetadata(meta Metadata) bool {
	return meta.DeliveryID != "" && len(meta.DeliveryID) <= 256 && !meta.CreatedAt.IsZero() && safeLine(meta.DeliveryID, 256)
}

// Prepare freezes MIME bytes, Date and Message-ID. SingleEmail/BCC share a MIME
// message but still have one receipt per SMTP envelope recipient, allowing a
// later retry to exclude every recipient already accepted after DATA.
func (s *SMTP) Prepare(settings EmailSettings, content EmailContent, meta Metadata) (Prepared, error) {
	if !validateMetadata(meta) || !safeLine(content.Subject, 4096) || content.Subject == "" || (content.TextBody == "" && content.HTMLBody == "") || len(content.TextBody)+len(content.HTMLBody) > MaxPayloadBytes || len(content.Attachments) > 8 {
		return Prepared{}, ErrInvalidPrepared
	}
	addresses, err := emailAddresses(settings)
	if err != nil {
		return Prepared{}, err
	}
	for _, reply := range content.ReplyTo {
		if _, err := envelopeAddress(reply); err != nil {
			return Prepared{}, err
		}
	}
	if len(content.ReplyTo) > 0 && s.settings.StaticHeaders["Reply-To"] != "" {
		return Prepared{}, ErrInvalidPrepared
	}
	if len(content.ReplyTo) > 32 {
		return Prepared{}, ErrInvalidPrepared
	}
	contentSize := len(content.TextBody) + len(content.HTMLBody)
	for _, attachment := range content.Attachments {
		contentSize += len(attachment.Data)
		if contentSize > MaxPayloadBytes {
			return Prepared{}, ErrInvalidPrepared
		}
		if attachment.Name == "" || !safeLine(attachment.Name, 128) || strings.ContainsAny(attachment.Name, "/\\") || len(attachment.Data) > MaxPayloadBytes {
			return Prepared{}, ErrInvalidPrepared
		}
		media, params, err := mime.ParseMediaType(attachment.ContentType)
		if err != nil || media == "" || len(params) != 0 {
			return Prepared{}, ErrInvalidPrepared
		}
		if attachment.ContentID != "" && (!operationKey.MatchString(attachment.ContentID) || strings.ContainsRune(attachment.ContentID, ':')) {
			return Prepared{}, ErrInvalidPrepared
		}
	}
	operations := make([]Operation, 0, len(addresses))
	var shared []byte
	if settings.SingleEmail || s.settings.UseBCC {
		shared, err = s.mimeMessage(addresses, content, meta, "shared", s.settings.UseBCC && !settings.SingleEmail)
		if err != nil {
			return Prepared{}, err
		}
	}
	totalPrepared := 0
	for _, address := range addresses {
		payload := shared
		if payload == nil {
			payload, err = s.mimeMessage([]mail.Address{address}, content, meta, address.Address, false)
			if err != nil {
				return Prepared{}, err
			}
		}
		if !mimeLinesBounded(payload) {
			return Prepared{}, ErrInvalidPrepared
		}
		totalPrepared += len(payload)
		if totalPrepared > MaxPayloadBytes {
			return Prepared{}, ErrInvalidPrepared
		}
		operations = append(operations, Operation{Key: recipientKey(address.Address), Target: address.Address, Payload: payload})
	}
	return NewPrepared("smtp", operations)
}

func mimeLinesBounded(data []byte) bool {
	for _, line := range bytes.Split(data, []byte("\r\n")) {
		if len(line) > 998 || bytes.ContainsAny(line, "\r\n") {
			return false
		}
	}
	return true
}
func normalizeCRLF(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\n", "\r\n")
}
func writeQP(writer io.Writer, value string) error {
	q := quotedprintable.NewWriter(writer)
	if _, err := io.WriteString(q, normalizeCRLF(value)); err != nil {
		return err
	}
	return q.Close()
}
func (s *SMTP) mimeMessage(addresses []mail.Address, content EmailContent, meta Metadata, identity string, bcc bool) ([]byte, error) {
	var body bytes.Buffer
	media := "text/plain; charset=utf-8"
	if len(content.Attachments) > 0 {
		mixed := multipart.NewWriter(&body)
		media = mime.FormatMediaType("multipart/mixed", map[string]string{"boundary": mixed.Boundary()})
		contentType, contentBytes, err := mimeContent(content)
		if err != nil {
			return nil, ErrInvalidPrepared
		}
		h := textproto.MIMEHeader{"Content-Type": {contentType}}
		if !strings.HasPrefix(contentType, "multipart/") {
			h.Set("Content-Transfer-Encoding", "quoted-printable")
		}
		part, err := mixed.CreatePart(h)
		if err != nil {
			return nil, ErrInvalidPrepared
		}
		if _, err = part.Write(contentBytes); err != nil {
			return nil, ErrInvalidPrepared
		}
		for _, a := range content.Attachments {
			disposition := "attachment"
			if a.ContentID != "" {
				disposition = "inline"
			}
			h := textproto.MIMEHeader{"Content-Type": {a.ContentType}, "Content-Disposition": {mime.FormatMediaType(disposition, map[string]string{"filename": a.Name})}, "Content-Transfer-Encoding": {"base64"}}
			if a.ContentID != "" {
				h.Set("Content-ID", "<"+a.ContentID+">")
			}
			part, err := mixed.CreatePart(h)
			if err != nil {
				return nil, ErrInvalidPrepared
			}
			encoded := base64.StdEncoding.EncodeToString(a.Data)
			for len(encoded) > 0 {
				n := min(76, len(encoded))
				if _, err := io.WriteString(part, encoded[:n]+"\r\n"); err != nil {
					return nil, ErrInvalidPrepared
				}
				encoded = encoded[n:]
			}
		}
		if mixed.Close() != nil {
			return nil, ErrInvalidPrepared
		}
	} else {
		var err error
		media, sharedBody, err := mimeContent(content)
		if err != nil {
			return nil, ErrInvalidPrepared
		}
		body.Write(sharedBody)
		return s.mimeEnvelope(addresses, content, meta, identity, bcc, media, body.Bytes()), nil
	}
	return s.mimeEnvelope(addresses, content, meta, identity, bcc, media, body.Bytes()), nil
}
func mimeContent(content EmailContent) (string, []byte, error) {
	var body bytes.Buffer
	if content.TextBody != "" && content.HTMLBody != "" {
		alternative := multipart.NewWriter(&body)
		for _, item := range []struct{ kind, value string }{{"text/plain", content.TextBody}, {"text/html", content.HTMLBody}} {
			part, err := alternative.CreatePart(textproto.MIMEHeader{"Content-Type": {item.kind + "; charset=utf-8"}, "Content-Transfer-Encoding": {"quoted-printable"}})
			if err != nil {
				return "", nil, err
			}
			if err = writeQP(part, item.value); err != nil {
				return "", nil, err
			}
		}
		if err := alternative.Close(); err != nil {
			return "", nil, err
		}
		return mime.FormatMediaType("multipart/alternative", map[string]string{"boundary": alternative.Boundary()}), body.Bytes(), nil
	}
	kind, value := "text/plain", content.TextBody
	if content.HTMLBody != "" {
		kind, value = "text/html", content.HTMLBody
	}
	if err := writeQP(&body, value); err != nil {
		return "", nil, err
	}
	return kind + "; charset=utf-8", body.Bytes(), nil
}
func (s *SMTP) mimeEnvelope(addresses []mail.Address, content EmailContent, meta Metadata, identity string, bcc bool, contentType string, body []byte) []byte {
	var result bytes.Buffer
	write := func(key, value string) { result.WriteString(key + ": " + value + "\r\n") }
	write("From", s.from.String())
	to := []string{}
	for _, a := range addresses {
		to = append(to, a.String())
	}
	if bcc {
		write("To", "undisclosed-recipients:;")
	} else {
		write("To", strings.Join(to, ",\r\n "))
	}
	if len(content.ReplyTo) > 0 {
		values := []string{}
		for _, value := range content.ReplyTo {
			a, _ := envelopeAddress(value)
			values = append(values, a.String())
		}
		write("Reply-To", strings.Join(values, ",\r\n "))
	}
	write("Subject", mime.QEncoding.Encode("utf-8", content.Subject))
	write("Date", meta.CreatedAt.UTC().Format(time.RFC1123Z))
	sum := sha256.Sum256([]byte(meta.DeliveryID + "\x00" + identity))
	write("Message-ID", "<"+hex.EncodeToString(sum[:])+"@metricspanel233.local>")
	keys := []string{}
	for key := range s.settings.StaticHeaders {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		write(key, s.settings.StaticHeaders[key])
	}
	write("MIME-Version", "1.0")
	write("Content-Type", contentType)
	if !strings.HasPrefix(contentType, "multipart/") {
		write("Content-Transfer-Encoding", "quoted-printable")
	}
	result.WriteString("\r\n")
	result.Write(body)
	if !bytes.HasSuffix(body, []byte("\r\n")) {
		result.WriteString("\r\n")
	}
	return result.Bytes()
}

func (s *SMTP) validOperation(op Operation) bool {
	a, err := envelopeAddress(op.Target)
	if err != nil || a.Address != op.Target || op.Key != recipientKey(op.Target) {
		return false
	}
	if !mimeLinesBounded(op.Payload) {
		return false
	}
	message, err := mail.ReadMessage(bytes.NewReader(op.Payload))
	if err != nil {
		return false
	}
	from, err := mail.ParseAddress(message.Header.Get("From"))
	return err == nil && from.Address == s.from.Address && smtpMessageID.MatchString(message.Header.Get("Message-ID"))
}
func (s *SMTP) Send(parent context.Context, prepared Prepared, progress Progress) []Outcome {
	return s.SendJournaled(parent, prepared, progress, nil)
}
func (s *SMTP) SendJournaled(parent context.Context, prepared Prepared, progress Progress, checkpoint Checkpoint) []Outcome {
	ops := prepared.Operations()
	if len(ops) == 0 {
		return []Outcome{outcomeFailure("", "invalid_prepared", false)}
	}
	out := make([]Outcome, len(ops))
	valid := prepared.Protocol() == "smtp" && len(ops) > 0
	for _, op := range ops {
		valid = valid && s.validOperation(op)
	}
	if !valid {
		for i, op := range ops {
			out[i] = outcomeFailure(op.Key, "invalid_prepared", false)
		}
		return out
	}
	ctx, cancel := boundedContext(parent, s.settings.Timeout)
	defer cancel()
	groups := [][]int{}
	lookup := map[[32]byte]int{}
	for i, op := range ops {
		if receipt, ok := progress[op.Key]; ok {
			out[i] = outcomeSent(op.Key, receipt, true)
			continue
		}
		sum := sha256.Sum256(op.Payload)
		index, exists := lookup[sum]
		if !exists {
			index = len(groups)
			lookup[sum] = index
			groups = append(groups, nil)
		}
		groups[index] = append(groups[index], i)
	}
	// Sequential groups bound concurrency and preserve successful earlier groups.
	for _, indices := range groups {
		if ctx.Err() != nil {
			for _, index := range indices {
				out[index] = smtpFailure(ctx, ops[index].Key, "smtp_connection", ctx.Err())
			}
			continue
		}
		client, closeClient, err := s.connect(ctx)
		if err != nil {
			for _, index := range indices {
				out[index] = smtpFailure(ctx, ops[index].Key, "smtp_connection", err)
			}
			continue
		}
		first := ops[indices[0]]
		if err = client.Mail(s.from.Address); err != nil {
			for _, index := range indices {
				out[index] = smtpFailure(ctx, ops[index].Key, "smtp_mail", err)
			}
			closeClient()
			continue
		}
		accepted := []int{}
		for _, index := range indices {
			if err = client.Rcpt(ops[index].Target); err != nil {
				out[index] = smtpFailure(ctx, ops[index].Key, "smtp_recipient", err)
			} else {
				accepted = append(accepted, index)
			}
		}
		checkpointFailed := false
		if len(accepted) > 0 {
			writer, dataErr := client.Data()
			if dataErr == nil {
				_, dataErr = writer.Write(first.Payload)
				if dataErr == nil {
					dataErr = writer.Close()
				} else {
					_ = client.Close()
				}
			}
			for _, index := range accepted {
				if dataErr != nil {
					out[index] = smtpFailure(ctx, ops[index].Key, "smtp_data", dataErr)
				} else {
					message, _ := mail.ReadMessage(bytes.NewReader(first.Payload))
					out[index] = outcomeSent(ops[index].Key, Receipt{ID: message.Header.Get("Message-ID")}, false)
				}
			}
			if dataErr == nil {
				acknowledged := make([]Outcome, 0, len(accepted))
				for _, index := range accepted {
					acknowledged = append(acknowledged, out[index])
				}
				if !checkpointSuccessful(ctx, checkpoint, acknowledged) {
					checkpointFailed = true
					for _, index := range accepted {
						out[index].Code = "checkpoint_failed"
					}
				}
			}
		}
		closeClient()
		if checkpointFailed {
			for index, op := range ops {
				if out[index].Code == "" {
					out[index] = outcomeFailure(op.Key, "checkpoint_blocked", true)
				}
			}
			break
		}
	}
	return out
}

type smtpFixedError string

func (e smtpFixedError) Error() string { return string(e) }

type smtpStageError struct {
	code  string
	cause error
}

func (e smtpStageError) Error() string { return e.code }
func (e smtpStageError) Unwrap() error { return e.cause }
func (s *SMTP) connect(ctx context.Context) (*smtp.Client, func(), error) {
	raw, err := (&net.Dialer{}).DialContext(ctx, "tcp", s.settings.Host)
	if err != nil {
		return nil, nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = raw.Close() })
	closeRaw := func() { stop(); _ = raw.Close() }
	if deadline, ok := ctx.Deadline(); ok {
		if err = raw.SetDeadline(deadline); err != nil {
			closeRaw()
			return nil, nil, err
		}
	}
	var conn net.Conn = raw
	if s.settings.ImplicitTLS {
		secure := tls.Client(raw, s.tls.Clone())
		if err = secure.HandshakeContext(ctx); err != nil {
			closeRaw()
			return nil, nil, smtpStageError{"smtp_tls", err}
		}
		conn = secure
	}
	client, err := smtp.NewClient(conn, s.host)
	if err != nil {
		closeRaw()
		return nil, nil, smtpStageError{"smtp_greeting", err}
	}
	closeClient := func() { stop(); _ = client.Close() }
	fail := func(err error) (*smtp.Client, func(), error) { closeClient(); return nil, nil, err }
	if err = client.Hello(s.settings.EhloIdentity); err != nil {
		return fail(smtpStageError{"smtp_greeting", err})
	}
	if !s.settings.ImplicitTLS && s.settings.StartTLSPolicy != NoStartTLS {
		if supported, _ := client.Extension("STARTTLS"); supported {
			if err = client.StartTLS(s.tls.Clone()); err != nil {
				return fail(smtpStageError{"smtp_tls", err})
			}
		} else if s.settings.StartTLSPolicy == MandatoryStartTLS {
			return fail(smtpFixedError("smtp_tls_required"))
		}
	}
	if s.settings.AuthUser != "" {
		if _, secure := client.TLSConnectionState(); !secure {
			return fail(smtpFixedError("smtp_auth_requires_tls"))
		}
		supported, mechanisms := client.Extension("AUTH")
		if !supported {
			return fail(smtpFixedError("smtp_auth_unavailable"))
		}
		available := map[string]bool{}
		for _, m := range strings.Fields(mechanisms) {
			available[strings.ToUpper(m)] = true
		}
		mode := s.settings.AuthMode
		if mode == "auto" {
			switch {
			case available["CRAM-MD5"]:
				mode = "cram-md5"
			case available["PLAIN"]:
				mode = "plain"
			case available["LOGIN"]:
				mode = "login"
			default:
				return fail(smtpFixedError("smtp_auth_unavailable"))
			}
		}
		var auth smtp.Auth
		switch mode {
		case "plain":
			if !available["PLAIN"] {
				return fail(smtpFixedError("smtp_auth_unavailable"))
			}
			auth = smtp.PlainAuth("", s.settings.AuthUser, s.secrets.Password, s.host)
		case "cram-md5":
			if !available["CRAM-MD5"] {
				return fail(smtpFixedError("smtp_auth_unavailable"))
			}
			auth = smtp.CRAMMD5Auth(s.settings.AuthUser, s.secrets.Password)
		case "login":
			if !available["LOGIN"] {
				return fail(smtpFixedError("smtp_auth_unavailable"))
			}
			auth = &loginAuth{username: s.settings.AuthUser, password: s.secrets.Password}
		}
		if err = client.Auth(auth); err != nil {
			return fail(smtpStageError{"smtp_authentication", err})
		}
	}
	return client, closeClient, nil
}

type loginAuth struct {
	username, password string
	step               int
}

func (a *loginAuth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	if !server.TLS {
		return "", nil, smtpFixedError("smtp_auth_requires_tls")
	}
	return "LOGIN", nil, nil
}
func (a *loginAuth) Next(_ []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	a.step++
	switch a.step {
	case 1:
		return []byte(a.username), nil
	case 2:
		return []byte(a.password), nil
	default:
		return nil, smtpFixedError("smtp_auth_failure")
	}
}
func smtpFailure(ctx context.Context, key, stage string, err error) Outcome {
	if ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return outcomeFailure(key, "timeout", true)
		}
		return outcomeFailure(key, "cancelled", true)
	}
	var fixed smtpFixedError
	if errors.As(err, &fixed) {
		return outcomeFailure(key, string(fixed), false)
	}
	var atStage smtpStageError
	if errors.As(err, &atStage) {
		stage = atStage.code
	}
	var response *textproto.Error
	if errors.As(err, &response) {
		out := outcomeFailure(key, stage, response.Code >= 400 && response.Code < 500)
		out.StatusCode = response.Code
		return out
	}
	var unknown x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	var verify *tls.CertificateVerificationError
	if errors.As(err, &unknown) || errors.As(err, &hostname) || errors.As(err, &invalid) || errors.As(err, &verify) {
		return outcomeFailure(key, "tls_verification", false)
	}
	return outcomeFailure(key, stage, true)
}

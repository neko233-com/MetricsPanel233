package receivers

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/textproto"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type smtpCapture struct {
	recipients []string
	data       []byte
	secure     bool
}
type smtpFixture struct {
	listener                     net.Listener
	mu                           sync.Mutex
	connections                  map[net.Conn]bool
	wg                           sync.WaitGroup
	captures                     []smtpCapture
	rcptAttempts                 map[string]int
	startTLS, implicitTLS        bool
	tlsConfig                    *tls.Config
	authMode, username, password string
	authOK, authAttempts         int
	authReject, dataReject       int
	stall                        string
}

func fixtureCertificate(t *testing.T) (tls.Certificate, string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	require.NoError(t, err)
	certificate := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "receiver.test"}, DNSNames: []string{"localhost", "receiver.test"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, certificate, certificate, &key.PublicKey, key)
	require.NoError(t, err)
	encodedKey, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	certPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encodedKey}))
	parsed, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	require.NoError(t, err)
	return parsed, certPEM, keyPEM
}
func newSMTPFixture(t *testing.T, configure func(*smtpFixture)) *smtpFixture {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	f := &smtpFixture{listener: listener, connections: map[net.Conn]bool{}, rcptAttempts: map[string]int{}}
	if configure != nil {
		configure(f)
	}
	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			f.mu.Lock()
			f.connections[conn] = true
			f.wg.Add(1)
			f.mu.Unlock()
			go func() {
				defer f.wg.Done()
				defer func() { _ = conn.Close(); f.mu.Lock(); delete(f.connections, conn); f.mu.Unlock() }()
				f.serve(conn)
			}()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		f.mu.Lock()
		for conn := range f.connections {
			_ = conn.Close()
		}
		f.mu.Unlock()
		f.wg.Wait()
	})
	return f
}
func (f *smtpFixture) serve(raw net.Conn) {
	_ = raw.SetDeadline(time.Now().Add(5 * time.Second))
	if f.stall == "greeting" {
		_, _ = io.Copy(io.Discard, raw)
		return
	}
	var conn net.Conn = raw
	secure := false
	if f.implicitTLS {
		tlsConn := tls.Server(raw, f.tlsConfig)
		if tlsConn.Handshake() != nil {
			return
		}
		conn = tlsConn
		secure = true
	}
	text := textproto.NewConn(conn)
	defer text.Close()
	_ = text.PrintfLine("220 receiver.test ESMTP")
	recipients := []string{}
	for {
		line, err := text.ReadLine()
		if err != nil {
			return
		}
		command := strings.ToUpper(strings.SplitN(line, " ", 2)[0])
		if f.stall == strings.ToLower(command) {
			_, _ = io.Copy(io.Discard, conn)
			return
		}
		switch command {
		case "EHLO":
			_ = text.PrintfLine("250-receiver.test")
			if f.startTLS && !secure {
				_ = text.PrintfLine("250-STARTTLS")
			}
			if f.authMode != "" {
				_ = text.PrintfLine("250-AUTH %s", strings.ToUpper(f.authMode))
			}
			_ = text.PrintfLine("250 8BITMIME")
		case "HELO":
			_ = text.PrintfLine("250 receiver.test")
		case "STARTTLS":
			if !f.startTLS || secure {
				_ = text.PrintfLine("454 unavailable")
				continue
			}
			_ = text.PrintfLine("220 start TLS")
			tlsConn := tls.Server(conn, f.tlsConfig)
			if tlsConn.Handshake() != nil {
				return
			}
			conn = tlsConn
			secure = true
			text = textproto.NewConn(conn)
		case "AUTH":
			f.mu.Lock()
			f.authAttempts++
			f.mu.Unlock()
			if f.authReject != 0 {
				_ = text.PrintfLine("%d private-password provider-private", f.authReject)
				continue
			}
			parts := strings.Fields(line)
			valid := false
			switch parts[1] {
			case "PLAIN":
				encoded := ""
				if len(parts) > 2 {
					encoded = parts[2]
				} else {
					_ = text.PrintfLine("334 ")
					encoded, _ = text.ReadLine()
				}
				decoded, _ := base64.StdEncoding.DecodeString(encoded)
				valid = string(decoded) == "\x00"+f.username+"\x00"+f.password
			case "LOGIN":
				_ = text.PrintfLine("334 %s", base64.StdEncoding.EncodeToString([]byte("Username:")))
				name, _ := text.ReadLine()
				_ = text.PrintfLine("334 %s", base64.StdEncoding.EncodeToString([]byte("Password:")))
				pass, _ := text.ReadLine()
				u, _ := base64.StdEncoding.DecodeString(name)
				p, _ := base64.StdEncoding.DecodeString(pass)
				valid = string(u) == f.username && string(p) == f.password
			case "CRAM-MD5":
				challenge := []byte("<local-fixture@receiver.test>")
				_ = text.PrintfLine("334 %s", base64.StdEncoding.EncodeToString(challenge))
				encoded, _ := text.ReadLine()
				decoded, _ := base64.StdEncoding.DecodeString(encoded)
				mac := hmac.New(md5.New, []byte(f.password))
				mac.Write(challenge)
				valid = string(decoded) == f.username+" "+hex.EncodeToString(mac.Sum(nil))
			}
			if valid {
				f.mu.Lock()
				f.authOK++
				f.mu.Unlock()
				_ = text.PrintfLine("235 authenticated")
			} else {
				_ = text.PrintfLine("535 private-password invalid")
			}
		case "MAIL":
			recipients = nil
			_ = text.PrintfLine("250 sender accepted")
		case "RCPT":
			address := strings.TrimSuffix(strings.TrimPrefix(line, "RCPT TO:<"), ">")
			f.mu.Lock()
			f.rcptAttempts[address]++
			attempt := f.rcptAttempts[address]
			f.mu.Unlock()
			if strings.HasPrefix(address, "temporary@") && attempt == 1 {
				_ = text.PrintfLine("450 private-password later")
				continue
			}
			if strings.HasPrefix(address, "permanent@") {
				_ = text.PrintfLine("550 private-password rejected")
				continue
			}
			recipients = append(recipients, address)
			_ = text.PrintfLine("250 recipient accepted")
		case "DATA":
			_ = text.PrintfLine("354 send data")
			data, err := text.ReadDotBytes()
			if err != nil {
				return
			}
			if f.stall == "data-ack" {
				_, _ = io.Copy(io.Discard, conn)
				return
			}
			if f.dataReject != 0 {
				_ = text.PrintfLine("%d private-password body rejected", f.dataReject)
				continue
			}
			f.mu.Lock()
			f.captures = append(f.captures, smtpCapture{append([]string{}, recipients...), []byte(normalizeCRLF(string(data))), secure})
			f.mu.Unlock()
			_ = text.PrintfLine("250 accepted queued")
		case "RSET":
			recipients = nil
			_ = text.PrintfLine("250 reset")
		case "QUIT":
			_ = text.PrintfLine("221 goodbye")
			return
		case "*":
			_ = text.PrintfLine("501 authentication aborted")
		default:
			_ = text.PrintfLine("500 unknown command")
		}
	}
}
func (f *smtpFixture) snapshot() []smtpCapture {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make([]smtpCapture, len(f.captures))
	copy(result, f.captures)
	return result
}
func smtpTransport(t *testing.T, f *smtpFixture, edit func(*SMTPSettings), secret SMTPSecrets) *SMTP {
	t.Helper()
	settings := SMTPSettings{Host: f.listener.Addr().String(), FromAddress: "alerts@example.test", FromName: "监控", StartTLSPolicy: NoStartTLS, Timeout: time.Second}
	if edit != nil {
		edit(&settings)
	}
	sender, err := NewSMTP(settings, secret)
	require.NoError(t, err)
	return sender
}
func smtpPrepared(t *testing.T, sender *SMTP, addresses string, single bool) Prepared {
	t.Helper()
	prepared, err := sender.Prepare(EmailSettings{Addresses: addresses, SingleEmail: single}, EmailContent{Subject: "服务恢复 / example", TextBody: "first\n.second\n第三行", HTMLBody: "<p>服务恢复</p>"}, Metadata{DeliveryID: "frozen-delivery-233", CreatedAt: time.Unix(233000, 0)})
	require.NoError(t, err)
	return prepared
}

func TestSMTPPartialReceiptsRestartExcludeAcceptedRecipients(t *testing.T) {
	f := newSMTPFixture(t, nil)
	sender := smtpTransport(t, f, nil, SMTPSecrets{})
	prepared := smtpPrepared(t, sender, "accepted@example.test;temporary@example.test\npermanent@example.test", true)
	first := sender.Send(context.Background(), prepared, nil)
	require.Len(t, first, 3)
	require.True(t, first[0].Sent)
	require.True(t, first[1].Retryable)
	require.Equal(t, 450, first[1].StatusCode)
	require.False(t, first[2].Retryable)
	require.Equal(t, 550, first[2].StatusCode)
	encoded, err := json.Marshal(first)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "private-password")
	require.NotContains(t, string(encoded), f.listener.Addr().String())
	raw, err := json.Marshal(prepared)
	require.NoError(t, err)
	var restored Prepared
	require.NoError(t, json.Unmarshal(raw, &restored))
	// The caller retries only unfinished retryable work; retain one accepted
	// operation to prove the durable receipt prevents it being sent again.
	operations := restored.Operations()
	retry, err := NewPrepared("smtp", operations[:2])
	require.NoError(t, err)
	second := sender.Send(context.Background(), retry, Progress{first[0].OperationKey: first[0].Receipt})
	require.True(t, second[0].Skipped)
	require.True(t, second[1].Sent)
	captures := f.snapshot()
	require.Len(t, captures, 2)
	require.Equal(t, []string{"accepted@example.test"}, captures[0].recipients)
	require.Equal(t, []string{"temporary@example.test"}, captures[1].recipients)
	require.Equal(t, captures[0].data, captures[1].data)
	require.Equal(t, first[0].Receipt.ID, second[1].Receipt.ID, "singleEmail shares stable Message-ID across partial retries")
	message, err := mail.ReadMessage(strings.NewReader(string(captures[0].data)))
	require.NoError(t, err)
	subject, err := new(mime.WordDecoder).DecodeHeader(message.Header.Get("Subject"))
	require.NoError(t, err)
	require.Equal(t, "服务恢复 / example", subject)
	media, params, err := mime.ParseMediaType(message.Header.Get("Content-Type"))
	require.NoError(t, err)
	require.Equal(t, "multipart/alternative", media)
	reader := multipart.NewReader(message.Body, params["boundary"])
	plain, err := reader.NextPart()
	require.NoError(t, err)
	body, err := io.ReadAll(plain)
	require.NoError(t, err)
	require.Contains(t, string(body), ".second")
	require.Contains(t, string(body), "第三行")
	html, err := reader.NextPart()
	require.NoError(t, err)
	body, err = io.ReadAll(html)
	require.NoError(t, err)
	require.Equal(t, "<p>服务恢复</p>", string(body))
}
func TestSMTPSeparateRecipientsBCCAndFrozenAttachments(t *testing.T) {
	for _, bcc := range []bool{false, true} {
		t.Run(map[bool]string{false: "individual", true: "bcc"}[bcc], func(t *testing.T) {
			f := newSMTPFixture(t, nil)
			headers := map[string]string{"X-Trace": "original"}
			settings := SMTPSettings{Host: f.listener.Addr().String(), FromAddress: "alerts@example.test", StartTLSPolicy: NoStartTLS, UseBCC: bcc, StaticHeaders: headers}
			sender, err := NewSMTP(settings, SMTPSecrets{})
			require.NoError(t, err)
			headers["X-Trace"] = "mutated"
			image := []byte("image frozen")
			content := EmailContent{Subject: "attachment", HTMLBody: "<img src=\"cid:image1\">", Attachments: []EmailAttachment{{Name: "图像.png", ContentType: "image/png", ContentID: "image1", Data: image}}}
			prepared, err := sender.Prepare(EmailSettings{Addresses: "one@example.test,two@example.test"}, content, Metadata{DeliveryID: "attachments", CreatedAt: time.Unix(233, 0)})
			require.NoError(t, err)
			image[0] = 'X'
			content.HTMLBody = "changed"
			out := sender.Send(context.Background(), prepared, nil)
			require.True(t, out[0].Sent)
			require.True(t, out[1].Sent)
			captures := f.snapshot()
			if bcc {
				require.Len(t, captures, 1)
			} else {
				require.Len(t, captures, 2)
			}
			message, err := mail.ReadMessage(strings.NewReader(string(captures[0].data)))
			require.NoError(t, err)
			require.Equal(t, "original", message.Header.Get("X-Trace"))
			require.Empty(t, message.Header.Get("Bcc"))
			if bcc {
				require.Equal(t, "undisclosed-recipients:;", message.Header.Get("To"))
			} else {
				require.NotContains(t, message.Header.Get("To"), "two@example.test")
			}
			_, params, err := mime.ParseMediaType(message.Header.Get("Content-Type"))
			require.NoError(t, err)
			reader := multipart.NewReader(message.Body, params["boundary"])
			part, err := reader.NextPart()
			require.NoError(t, err)
			body, err := io.ReadAll(part)
			require.NoError(t, err)
			require.Contains(t, string(body), "cid:image1")
			part, err = reader.NextPart()
			require.NoError(t, err)
			require.Equal(t, "<image1>", part.Header.Get("Content-ID"))
			body, err = io.ReadAll(base64.NewDecoder(base64.StdEncoding, part))
			require.NoError(t, err)
			require.Equal(t, "image frozen", string(body))
		})
	}
}
func TestSMTPTLSAndAuthentication(t *testing.T) {
	for _, mode := range []string{"PLAIN", "LOGIN", "CRAM-MD5"} {
		for _, implicit := range []bool{false, true} {
			t.Run(mode+map[bool]string{false: "-starttls", true: "-implicit"}[implicit], func(t *testing.T) {
				certificate, ca, _ := fixtureCertificate(t)
				f := newSMTPFixture(t, func(f *smtpFixture) {
					f.startTLS = !implicit
					f.implicitTLS = implicit
					f.tlsConfig = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
					f.authMode = mode
					f.username = "user"
					f.password = "private-password"
				})
				sender := smtpTransport(t, f, func(s *SMTPSettings) {
					s.StartTLSPolicy = MandatoryStartTLS
					s.ImplicitTLS = implicit
					s.AuthUser = "user"
					s.AuthMode = strings.ToLower(mode)
					s.TLS = TLSSettings{CAPEM: ca}
				}, SMTPSecrets{Password: "private-password"})
				out := sender.Send(context.Background(), smtpPrepared(t, sender, "one@example.test", false), nil)
				require.True(t, out[0].Sent)
				f.mu.Lock()
				require.Equal(t, 1, f.authOK)
				f.mu.Unlock()
				require.True(t, f.snapshot()[0].secure)
			})
		}
	}
}
func TestSMTPMutualTLSAndPermanentCertificateFailure(t *testing.T) {
	cert, ca, key := fixtureCertificate(t)
	pool := x509.NewCertPool()
	require.True(t, pool.AppendCertsFromPEM([]byte(ca)))
	f := newSMTPFixture(t, func(f *smtpFixture) {
		f.implicitTLS = true
		f.tlsConfig = &tls.Config{Certificates: []tls.Certificate{cert}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool, MinVersion: tls.VersionTLS12}
	})
	sender := smtpTransport(t, f, func(s *SMTPSettings) { s.ImplicitTLS = true; s.TLS = TLSSettings{CAPEM: ca, CertPEM: ca, KeyPEM: key} }, SMTPSecrets{})
	good := sender.Send(context.Background(), smtpPrepared(t, sender, "one@example.test", false), nil)
	require.True(t, good[0].Sent)
	bad := smtpTransport(t, f, func(s *SMTPSettings) { s.ImplicitTLS = true }, SMTPSecrets{})
	out := bad.Send(context.Background(), smtpPrepared(t, bad, "two@example.test", false), nil)
	require.False(t, out[0].Sent)
	require.False(t, out[0].Retryable)
	require.Equal(t, "tls_verification", out[0].Code)
}
func TestSMTPPolicyAuthDataAndCancellationFailures(t *testing.T) {
	f := newSMTPFixture(t, nil)
	mandatory := smtpTransport(t, f, func(s *SMTPSettings) { s.StartTLSPolicy = MandatoryStartTLS }, SMTPSecrets{})
	out := mandatory.Send(context.Background(), smtpPrepared(t, mandatory, "one@example.test", false), nil)
	require.Equal(t, "smtp_tls_required", out[0].Code)
	require.False(t, out[0].Retryable)
	unencrypted := smtpTransport(t, f, func(s *SMTPSettings) { s.AuthUser = "user" }, SMTPSecrets{Password: "private-password"})
	out = unencrypted.Send(context.Background(), smtpPrepared(t, unencrypted, "one@example.test", false), nil)
	require.Equal(t, "smtp_auth_requires_tls", out[0].Code)
	require.Zero(t, f.authAttempts)
	for _, status := range []int{454, 535, 451, 554} {
		t.Run("status"+strconv.Itoa(status), func(t *testing.T) {
			cert, ca, _ := fixtureCertificate(t)
			fixture := newSMTPFixture(t, func(f *smtpFixture) {
				f.startTLS = true
				f.tlsConfig = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
				if status == 454 || status == 535 {
					f.authMode = "PLAIN"
					f.authReject = status
				} else {
					f.dataReject = status
				}
			})
			sender := smtpTransport(t, fixture, func(s *SMTPSettings) {
				s.StartTLSPolicy = MandatoryStartTLS
				s.TLS.CAPEM = ca
				if status == 454 || status == 535 {
					s.AuthUser = "user"
				}
			}, SMTPSecrets{})
			out := sender.Send(context.Background(), smtpPrepared(t, sender, "one@example.test", false), nil)
			require.False(t, out[0].Sent)
			require.Equal(t, status < 500, out[0].Retryable)
			require.Equal(t, status, out[0].StatusCode)
			raw, err := json.Marshal(out)
			require.NoError(t, err)
			require.NotContains(t, string(raw), "private-password")
		})
	}
	for _, stage := range []string{"greeting", "mail", "data-ack"} {
		t.Run("cancel-"+stage, func(t *testing.T) {
			fixture := newSMTPFixture(t, func(f *smtpFixture) { f.stall = stage })
			sender := smtpTransport(t, fixture, nil, SMTPSecrets{})
			ctx, cancel := context.WithCancel(context.Background())
			timer := time.AfterFunc(40*time.Millisecond, cancel)
			defer timer.Stop()
			start := time.Now()
			out := sender.Send(ctx, smtpPrepared(t, sender, "one@example.test", false), nil)
			require.Less(t, time.Since(start), 500*time.Millisecond)
			require.Equal(t, "cancelled", out[0].Code)
			require.True(t, out[0].Retryable)
			require.Empty(t, fixture.snapshot())
		})
	}
	t.Run("timeout", func(t *testing.T) {
		fixture := newSMTPFixture(t, func(f *smtpFixture) { f.stall = "greeting" })
		sender := smtpTransport(t, fixture, func(s *SMTPSettings) { s.Timeout = 30 * time.Millisecond }, SMTPSecrets{})
		start := time.Now()
		out := sender.Send(context.Background(), smtpPrepared(t, sender, "one@example.test", false), nil)
		require.Less(t, time.Since(start), 500*time.Millisecond)
		require.False(t, out[0].Sent)
		require.True(t, out[0].Retryable)
	})
}
func TestSMTPInvalidInputRejectedBeforeNetwork(t *testing.T) {
	for _, settings := range []SMTPSettings{{Host: "not-an-address", FromAddress: "one@example.test"}, {Host: "localhost:25", FromAddress: "one@example.test\r\nBcc: injected"}, {Host: "localhost:25", FromAddress: "one@example.test", StartTLSPolicy: "unknown"}, {Host: "localhost:25", FromAddress: "one@example.test", StaticHeaders: map[string]string{"Subject": "overridden"}}} {
		_, err := NewSMTP(settings, SMTPSecrets{})
		require.ErrorIs(t, err, ErrInvalidConfiguration)
	}
	f := newSMTPFixture(t, nil)
	sender := smtpTransport(t, f, nil, SMTPSecrets{})
	for _, addresses := range []string{"", "injected@example.test\r\nBcc: victim@example.test", "用户@example.test", strings.Repeat("one@example.test;", 33)} {
		_, err := sender.Prepare(EmailSettings{Addresses: addresses}, EmailContent{Subject: "valid", TextBody: "body"}, Metadata{DeliveryID: "id", CreatedAt: time.Now()})
		require.Error(t, err)
	}
	prepared := smtpPrepared(t, sender, "one@example.test", false)
	ops := prepared.Operations()
	ops[0].Target = "bad\r\ncommand"
	corrupt, err := NewPrepared("smtp", ops)
	require.NoError(t, err)
	out := sender.Send(context.Background(), corrupt, nil)
	require.Equal(t, "invalid_prepared", out[0].Code)
	require.Empty(t, f.snapshot())
	message, err := mail.ReadMessage(strings.NewReader(string(prepared.Operations()[0].Payload)))
	require.NoError(t, err)
	_, err = io.ReadAll(quotedprintable.NewReader(message.Body))
	require.NoError(t, err)
}

func TestSMTPJournaledReceiptDurableRestartBeforeNextRecipient(t *testing.T) {
	f := newSMTPFixture(t, nil)
	sender := smtpTransport(t, f, nil, SMTPSecrets{})
	prepared := smtpPrepared(t, sender, "one@example.test,two@example.test", false)
	checkpointFile := filepath.Join(t.TempDir(), "receipts.json")
	persisted := Progress{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := sender.SendJournaled(ctx, prepared, nil, func(_ context.Context, outcomes []Outcome) error {
		require.Len(t, f.snapshot(), 1, "checkpoint occurs before the next SMTP DATA transaction")
		for _, outcome := range outcomes {
			persisted[outcome.OperationKey] = outcome.Receipt
		}
		raw, err := json.Marshal(persisted)
		if err != nil {
			return err
		}
		if err = os.WriteFile(checkpointFile, raw, 0600); err != nil {
			return err
		}
		cancel()
		return nil
	})
	require.True(t, first[0].Sent)
	require.Equal(t, "cancelled", first[1].Code)
	require.Len(t, f.snapshot(), 1)
	raw, err := json.Marshal(prepared)
	require.NoError(t, err)
	var restored Prepared
	require.NoError(t, json.Unmarshal(raw, &restored))
	raw, err = os.ReadFile(checkpointFile)
	require.NoError(t, err)
	var progress Progress
	require.NoError(t, json.Unmarshal(raw, &progress))
	restarted := smtpTransport(t, f, nil, SMTPSecrets{})
	second := restarted.SendJournaled(context.Background(), restored, progress, func(_ context.Context, outcomes []Outcome) error {
		require.Len(t, outcomes, 1)
		require.Equal(t, recipientKey("two@example.test"), outcomes[0].OperationKey)
		return nil
	})
	require.True(t, second[0].Skipped)
	require.True(t, second[1].Sent)
	captures := f.snapshot()
	require.Len(t, captures, 2)
	require.Equal(t, []string{"one@example.test"}, captures[0].recipients)
	require.Equal(t, []string{"two@example.test"}, captures[1].recipients)
}
func TestSMTPCheckpointFailureStopsLaterNetworkAndBatchIsAtomic(t *testing.T) {
	f := newSMTPFixture(t, nil)
	sender := smtpTransport(t, f, nil, SMTPSecrets{})
	prepared := smtpPrepared(t, sender, "one@example.test,two@example.test", false)
	out := sender.SendJournaled(context.Background(), prepared, nil, func(context.Context, []Outcome) error {
		return errors.New("private-password " + f.listener.Addr().String())
	})
	require.True(t, out[0].Sent)
	require.Equal(t, "checkpoint_failed", out[0].Code)
	require.False(t, out[1].Sent)
	require.Equal(t, "checkpoint_blocked", out[1].Code)
	require.Len(t, f.snapshot(), 1)
	raw, err := json.Marshal(out)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "private-password")
	require.NotContains(t, string(raw), f.listener.Addr().String())
	batch := smtpPrepared(t, sender, "three@example.test,four@example.test", true)
	calls := 0
	out = sender.SendJournaled(context.Background(), batch, nil, func(_ context.Context, receipts []Outcome) error {
		calls++
		require.Len(t, receipts, 2)
		require.True(t, receipts[0].Sent)
		receipts[0].Sent = false
		return nil
	})
	require.Equal(t, 1, calls)
	require.True(t, out[0].Sent)
	require.True(t, out[1].Sent)
}

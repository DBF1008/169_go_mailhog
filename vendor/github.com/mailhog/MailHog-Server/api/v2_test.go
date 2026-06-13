package api

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/pat"
	"github.com/mailhog/MailHog-Server/config"
	"github.com/mailhog/data"
	"github.com/mailhog/storage"
)

// These tests exercise the APIv2 message workflows added so v2 no longer needs
// to fall back to APIv1: message detail, delete, raw download, MIME part
// download and release. They cover plain mail, multipart with an attachment,
// read-after-delete and both release success and failure.

const testHostname = "mailhog.test"

// newTestAPIv2 wires up an APIv2 backed by in-memory storage and returns a
// router that routes real requests through it (so path params are populated
// exactly as in production), along with the underlying store.
func newTestAPIv2(t *testing.T) (*pat.Router, *storage.InMemory) {
	t.Helper()
	store := storage.CreateInMemory()
	conf := &config.Config{
		Hostname:     testHostname,
		Storage:      store,
		MessageChan:  make(chan *data.Message),
		OutgoingSMTP: make(map[string]*config.OutgoingSMTP),
		WebPath:      "",
	}
	r := pat.New()
	createAPIv2(conf, r)
	return r, store
}

// storeMessage parses a raw SMTP payload (which triggers MIME parsing when the
// content type is multipart), assigns it a stable id, and stores it.
func storeMessage(t *testing.T, store *storage.InMemory, id, from, to, raw string) {
	t.Helper()
	smtpMsg := &data.SMTPMessage{
		Helo: "localhost",
		From: from,
		To:   []string{to},
		Data: raw,
	}
	msg := smtpMsg.Parse(testHostname)
	msg.ID = data.MessageID(id) // stable, URL-safe id for assertions
	if _, err := store.Store(msg); err != nil {
		t.Fatalf("storing message: %v", err)
	}
}

func doReq(t *testing.T, r http.Handler, method, target string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, body)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

const plainBody = "Hello plain world."

func plainRaw() string {
	return "Content-Type: text/plain; charset=utf-8\r\n" +
		"Subject: Plain hello\r\n" +
		"From: alice@example.com\r\n" +
		"To: bob@example.com\r\n" +
		"\r\n" +
		plainBody + "\r\n"
}

// multipartRaw returns a multipart/mixed message with a text part and a
// base64-encoded attachment. The decoded attachment content is returned too.
func multipartRaw() (raw, attachmentContent string) {
	attachmentContent = "hello world"
	b64 := base64.StdEncoding.EncodeToString([]byte(attachmentContent))
	boundary := "MixedBoundary"
	raw = "Subject: With attachment\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"" + boundary + "\"\r\n" +
		"\r\n" +
		"--" + boundary + "\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"This is the text body.\r\n" +
		"--" + boundary + "\r\n" +
		"Content-Type: application/octet-stream\r\n" +
		"Content-Disposition: attachment; filename=\"hello.txt\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"\r\n" + b64 + "\r\n" +
		"--" + boundary + "--\r\n"
	return raw, attachmentContent
}

func TestV2MessageDetailPlain(t *testing.T) {
	r, store := newTestAPIv2(t)
	storeMessage(t, store, "plain1", "alice@example.com", "bob@example.com", plainRaw())

	rec := doReq(t, r, "GET", "/api/v2/messages/plain1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var m data.Message
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("unmarshal: %v; body=%q", err, rec.Body.String())
	}
	if string(m.ID) != "plain1" {
		t.Errorf("id = %q, want plain1", m.ID)
	}
	if m.Content == nil || !strings.Contains(m.Content.Body, plainBody) {
		t.Errorf("body does not contain %q: %+v", plainBody, m.Content)
	}
}

func TestV2MessageDetailNotFound(t *testing.T) {
	r, _ := newTestAPIv2(t)

	rec := doReq(t, r, "GET", "/api/v2/messages/does-not-exist", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestV2DownloadRaw(t *testing.T) {
	r, store := newTestAPIv2(t)
	storeMessage(t, store, "plain1", "alice@example.com", "bob@example.com", plainRaw())

	rec := doReq(t, r, "GET", "/api/v2/messages/plain1/download", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "message/rfc822" {
		t.Errorf("Content-Type = %q, want message/rfc822", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "plain1.eml") {
		t.Errorf("Content-Disposition = %q, want it to mention plain1.eml", cd)
	}
	out := rec.Body.String()
	if !strings.Contains(out, "Subject: Plain hello") {
		t.Errorf("raw output missing Subject header: %q", out)
	}
	if !strings.Contains(out, plainBody) {
		t.Errorf("raw output missing body %q: %q", plainBody, out)
	}

	// Downloading a deleted/unknown message yields 404.
	rec = doReq(t, r, "GET", "/api/v2/messages/missing/download", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("download missing: status = %d, want 404", rec.Code)
	}
}

func TestV2MultipartDetailAndPartDownload(t *testing.T) {
	r, store := newTestAPIv2(t)
	raw, attachment := multipartRaw()
	storeMessage(t, store, "multi1", "alice@example.com", "bob@example.com", raw)

	// Detail exposes the parsed MIME parts.
	rec := doReq(t, r, "GET", "/api/v2/messages/multi1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("detail status = %d, want 200", rec.Code)
	}
	var m data.Message
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m.MIME == nil || len(m.MIME.Parts) < 2 {
		t.Fatalf("expected at least 2 MIME parts, got %+v", m.MIME)
	}

	// Part index 1 is the base64 attachment; it must come back decoded.
	rec = doReq(t, r, "GET", "/api/v2/messages/multi1/mime/part/1/download", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("part status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != attachment {
		t.Errorf("attachment body = %q, want %q", got, attachment)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "attachment") {
		t.Errorf("Content-Disposition = %q, want it to contain \"attachment\"", cd)
	}

	// Out-of-range and non-numeric part indexes must 404, not panic.
	if rec := doReq(t, r, "GET", "/api/v2/messages/multi1/mime/part/99/download", nil); rec.Code != http.StatusNotFound {
		t.Errorf("out-of-range part: status = %d, want 404", rec.Code)
	}
	if rec := doReq(t, r, "GET", "/api/v2/messages/multi1/mime/part/notanint/download", nil); rec.Code != http.StatusNotFound {
		t.Errorf("non-numeric part: status = %d, want 404", rec.Code)
	}
}

func TestV2DeleteThenRead(t *testing.T) {
	r, store := newTestAPIv2(t)
	storeMessage(t, store, "del1", "alice@example.com", "bob@example.com", plainRaw())

	if store.Count() != 1 {
		t.Fatalf("precondition: count = %d, want 1", store.Count())
	}

	// It exists first.
	if rec := doReq(t, r, "GET", "/api/v2/messages/del1", nil); rec.Code != http.StatusOK {
		t.Fatalf("pre-delete GET status = %d, want 200", rec.Code)
	}

	// Delete it.
	if rec := doReq(t, r, "DELETE", "/api/v2/messages/del1", nil); rec.Code != http.StatusOK {
		t.Fatalf("DELETE status = %d, want 200", rec.Code)
	}
	if store.Count() != 0 {
		t.Errorf("post-delete count = %d, want 0", store.Count())
	}

	// Reading it now 404s.
	if rec := doReq(t, r, "GET", "/api/v2/messages/del1", nil); rec.Code != http.StatusNotFound {
		t.Errorf("post-delete GET status = %d, want 404", rec.Code)
	}
}

func TestV2ReleaseSuccess(t *testing.T) {
	r, store := newTestAPIv2(t)
	storeMessage(t, store, "rel1", "alice@example.com", "bob@example.com", plainRaw())

	host, port, received, stop := startFakeSMTP(t)
	defer stop()

	body := mustJSON(t, map[string]interface{}{
		"Email": "dest@example.com",
		"Host":  host,
		"Port":  port,
	})
	rec := doReq(t, r, "POST", "/api/v2/messages/rel1/release", bytes.NewReader(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("release status = %d, want 200; body=%q", rec.Code, rec.Body.String())
	}

	select {
	case got := <-received:
		if !strings.Contains(got, plainBody) {
			t.Errorf("released message missing body %q; got %q", plainBody, got)
		}
	case <-time.After(2 * time.Second):
		t.Error("fake SMTP server never received the released message")
	}
}

func TestV2ReleaseFailure(t *testing.T) {
	r, store := newTestAPIv2(t)
	storeMessage(t, store, "relfail1", "alice@example.com", "bob@example.com", plainRaw())

	// A port with nothing listening => SMTP connect fails => 500.
	port := closedPort(t)
	body := mustJSON(t, map[string]interface{}{
		"Email": "dest@example.com",
		"Host":  "127.0.0.1",
		"Port":  port,
	})
	rec := doReq(t, r, "POST", "/api/v2/messages/relfail1/release", bytes.NewReader(body))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("release status = %d, want 500", rec.Code)
	}

	// Releasing a message that does not exist 404s.
	rec = doReq(t, r, "POST", "/api/v2/messages/nope/release", bytes.NewReader(body))
	if rec.Code != http.StatusNotFound {
		t.Errorf("release missing message: status = %d, want 404", rec.Code)
	}
}

func mustJSON(t *testing.T, v interface{}) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// startFakeSMTP starts a minimal SMTP server that accepts a single message and
// publishes the received DATA payload on the returned channel.
func startFakeSMTP(t *testing.T) (host, port string, received <-chan string, stop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	rcv := make(chan string, 1)

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		br := bufio.NewReader(conn)
		bw := bufio.NewWriter(conn)
		write := func(s string) {
			bw.WriteString(s + "\r\n")
			bw.Flush()
		}

		write("220 fake ESMTP ready")
		var data bytes.Buffer
		inData := false
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			if inData {
				if line == ".\r\n" || line == ".\n" {
					inData = false
					select {
					case rcv <- data.String():
					default:
					}
					write("250 OK: queued")
					continue
				}
				data.WriteString(line)
				continue
			}
			switch cmd := strings.ToUpper(strings.TrimSpace(line)); {
			case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
				write("250 fake.localhost")
			case strings.HasPrefix(cmd, "MAIL"):
				write("250 OK")
			case strings.HasPrefix(cmd, "RCPT"):
				write("250 OK")
			case strings.HasPrefix(cmd, "DATA"):
				write("354 End data with <CR><LF>.<CR><LF>")
				inData = true
			case strings.HasPrefix(cmd, "QUIT"):
				write("221 Bye")
				return
			case strings.HasPrefix(cmd, "RSET"), strings.HasPrefix(cmd, "NOOP"):
				write("250 OK")
			default:
				write("250 OK")
			}
		}
	}()

	host, port, err = net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("split host/port: %v", err)
	}
	return host, port, rcv, func() { ln.Close() }
}

// closedPort returns a TCP port on 127.0.0.1 that is guaranteed to have nothing
// listening (the listener is opened to reserve a free port, then closed).
func closedPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("split host/port: %v", err)
	}
	ln.Close()
	return port
}

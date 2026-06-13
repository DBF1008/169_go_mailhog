package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mailhog/MailHog-Server/config"
	"github.com/mailhog/data"
	"github.com/mailhog/storage"
)

// newMaildirAPI wires up the v1 and v2 API handlers backed by a fresh maildir
// storage in a temporary directory. The handler structs are constructed
// directly (rather than via createAPIv1/createAPIv2) so the tests don't spin up
// the event-stream/websocket goroutines, which are irrelevant here.
func newMaildirAPI(t *testing.T) (*APIv1, *APIv2, *storage.Maildir) {
	t.Helper()
	md := storage.CreateMaildir(t.TempDir())
	conf := &config.Config{
		Storage:      md,
		MessageChan:  make(chan *data.Message),
		OutgoingSMTP: make(map[string]*config.OutgoingSMTP),
	}
	return &APIv1{config: conf}, &APIv2{config: conf}, md
}

// apiStoreMessage stores a message directly through the storage backend and
// returns its ID.
func apiStoreMessage(t *testing.T, md *storage.Maildir, from, to, body string) string {
	t.Helper()
	smtp := &data.SMTPMessage{
		Helo: "localhost",
		From: from,
		To:   []string{to},
		Data: "Subject: Test\r\n\r\n" + body,
	}
	id, err := md.Store(smtp.Parse("mailhog.example"))
	if err != nil {
		t.Fatalf("Store failed: %s", err)
	}
	return id
}

// Empty directory: the v1 list endpoint must not 500 for a maildir backend
// (previously the type switch fell through to the default 500 branch).
func TestAPIv1MessagesMaildirEmpty(t *testing.T) {
	v1, _, _ := newMaildirAPI(t)

	req := httptest.NewRequest("GET", "http://example/api/v1/messages", nil)
	w := httptest.NewRecorder()
	v1.messages(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("v1 messages returned %d for an empty maildir, want 200; body=%q", w.Code, w.Body.String())
	}
	var msgs []data.Message
	if err := json.Unmarshal(w.Body.Bytes(), &msgs); err != nil {
		t.Fatalf("response is not valid JSON: %s; body=%q", err, w.Body.String())
	}
	if len(msgs) != 0 {
		t.Fatalf("expected 0 messages, got %d", len(msgs))
	}
}

// List read: the v1 list endpoint must return stored messages for a maildir.
func TestAPIv1MessagesMaildirReturnsStored(t *testing.T) {
	v1, _, md := newMaildirAPI(t)
	id := apiStoreMessage(t, md, "alice@example.com", "bob@example.com", "Hello Bob")

	req := httptest.NewRequest("GET", "http://example/api/v1/messages", nil)
	w := httptest.NewRecorder()
	v1.messages(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("v1 messages returned %d, want 200; body=%q", w.Code, w.Body.String())
	}
	var msgs []data.Message
	if err := json.Unmarshal(w.Body.Bytes(), &msgs); err != nil {
		t.Fatalf("response is not valid JSON: %s; body=%q", err, w.Body.String())
	}
	if len(msgs) != 1 || string(msgs[0].ID) != id {
		t.Fatalf("expected the stored message %s, got %+v", id, msgs)
	}
}

// List read via the v2 endpoint (the one the web UI uses): empty and populated.
func TestAPIv2MessagesMaildir(t *testing.T) {
	_, v2, md := newMaildirAPI(t)

	req := httptest.NewRequest("GET", "http://example/api/v2/messages", nil)
	w := httptest.NewRecorder()
	v2.messages(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("v2 messages on empty maildir returned %d, want 200", w.Code)
	}
	var res messagesResult
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("response is not valid JSON: %s; body=%q", err, w.Body.String())
	}
	if res.Total != 0 || res.Count != 0 || len(res.Items) != 0 {
		t.Fatalf("expected an empty result, got %+v", res)
	}

	id := apiStoreMessage(t, md, "alice@example.com", "bob@example.com", "Hello")

	req = httptest.NewRequest("GET", "http://example/api/v2/messages", nil)
	w = httptest.NewRecorder()
	v2.messages(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("v2 messages returned %d, want 200", w.Code)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("response is not valid JSON: %s; body=%q", err, w.Body.String())
	}
	if res.Total != 1 || len(res.Items) != 1 || string(res.Items[0].ID) != id {
		t.Fatalf("expected the stored message %s, got %+v", id, res)
	}
}

// A stray subdirectory in the maildir (Count sees it, but it isn't a message)
// must not make the v2 list endpoint panic/500 — the "visible count but can't
// open the mail" symptom.
func TestAPIv2MessagesMaildirWithSubdir(t *testing.T) {
	_, v2, md := newMaildirAPI(t)
	id := apiStoreMessage(t, md, "alice@example.com", "bob@example.com", "Hello")
	if err := os.Mkdir(filepath.Join(md.Path, "cur"), 0770); err != nil {
		t.Fatalf("Mkdir: %s", err)
	}

	req := httptest.NewRequest("GET", "http://example/api/v2/messages", nil)
	w := httptest.NewRecorder()
	v2.messages(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("v2 messages returned %d with a subdirectory present, want 200", w.Code)
	}
	var res messagesResult
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("response is not valid JSON: %s; body=%q", err, w.Body.String())
	}
	if len(res.Items) != 1 || string(res.Items[0].ID) != id {
		t.Fatalf("expected the stored message %s, got %+v", id, res)
	}
}

// Single download: the raw download endpoint must return the message instead of
// 500 for a maildir backend.
func TestAPIv1DownloadMaildir(t *testing.T) {
	v1, _, md := newMaildirAPI(t)
	id := apiStoreMessage(t, md, "alice@example.com", "bob@example.com", "Hello Bob")

	req := httptest.NewRequest("GET", "http://example/?:id="+url.QueryEscape(id), nil)
	w := httptest.NewRecorder()
	v1.download(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("download returned %d, want 200; body=%q", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "message/rfc822" {
		t.Fatalf("unexpected Content-Type: %q", ct)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Hello Bob") {
		t.Fatalf("download body missing message content: %q", body)
	}
}

// Delete then read: after a message is deleted, the single-message endpoint must
// respond gracefully (200 with a null body) rather than 500, and the list must
// still work.
func TestAPIv1MessageMaildirDeletedIsGraceful(t *testing.T) {
	v1, _, md := newMaildirAPI(t)
	id := apiStoreMessage(t, md, "alice@example.com", "bob@example.com", "Hello Bob")

	// Sanity: the message is readable before deletion.
	req := httptest.NewRequest("GET", "http://example/?:id="+url.QueryEscape(id), nil)
	w := httptest.NewRecorder()
	v1.message(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("reading the stored message returned %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), id) {
		t.Fatalf("expected the message body to contain its ID; body=%q", w.Body.String())
	}

	if err := md.DeleteOne(id); err != nil {
		t.Fatalf("DeleteOne: %s", err)
	}

	// Reading the now-deleted message must not 500.
	req = httptest.NewRequest("GET", "http://example/?:id="+url.QueryEscape(id), nil)
	w = httptest.NewRecorder()
	v1.message(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("reading a deleted message returned %d, want 200", w.Code)
	}
	if strings.TrimSpace(w.Body.String()) != "null" {
		t.Fatalf("expected a null body for a deleted message, got %q", w.Body.String())
	}

	// The list endpoint must still work and be empty.
	req = httptest.NewRequest("GET", "http://example/api/v1/messages", nil)
	w = httptest.NewRecorder()
	v1.messages(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("listing after delete returned %d, want 200", w.Code)
	}
	var msgs []data.Message
	if err := json.Unmarshal(w.Body.Bytes(), &msgs); err != nil {
		t.Fatalf("response is not valid JSON: %s; body=%q", err, w.Body.String())
	}
	if len(msgs) != 0 {
		t.Fatalf("expected an empty list after delete, got %d", len(msgs))
	}
}

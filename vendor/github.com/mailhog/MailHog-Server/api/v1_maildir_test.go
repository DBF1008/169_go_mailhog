package api

import (
	"encoding/json"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorilla/pat"
	"github.com/mailhog/MailHog-Server/config"
	"github.com/mailhog/data"
	"github.com/mailhog/storage"
)

// createTestAPIv1 creates an APIv1 for testing purposes
func createTestAPIv1(conf *config.Config) (*APIv1, *pat.Router) {
	r := pat.New()
	apiv1 := createAPIv1(conf, r)
	return apiv1, r
}

// newTestHTTPMessage creates a data.Message with raw SMTP data for storage
func newTestHTTPMessage(id, from, to, subject, body string) *data.Message {
	raw := &data.SMTPMessage{
		Helo: "test.example",
		From: from,
		To:   []string{to},
		Data: "From: " + from + "\r\nTo: " + to + "\r\nSubject: " + subject + "\r\n\r\n" + body,
	}
	msg := raw.Parse("mailhog.example")
	msg.ID = data.MessageID(id)
	msg.Raw = raw
	return msg
}

func setupMaildirAPI(t *testing.T) (*APIv1, *pat.Router, *storage.Maildir, func()) {
	t.Helper()

	dir, err := ioutil.TempDir("", "mailhog-api-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	maildir := storage.CreateMaildir(dir)
	conf := config.DefaultConfig()
	conf.Storage = maildir

	apiv1, router := createTestAPIv1(conf)

	cleanup := func() {
		os.RemoveAll(dir)
	}

	return apiv1, router, maildir, cleanup
}

func TestAPIv1MessagesWithMaildirEmpty(t *testing.T) {
	_, router, _, cleanup := setupMaildirAPI(t)
	defer cleanup()

	req := httptest.NewRequest("GET", "/api/v1/messages", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected status 200, got %d", w.Code)
	}

	var messages data.Messages
	err := json.Unmarshal(w.Body.Bytes(), &messages)
	if err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}
	if len(messages) != 0 {
		t.Fatalf("Expected 0 messages, got %d", len(messages))
	}
}

func TestAPIv1MessagesWithMaildir(t *testing.T) {
	_, router, maildir, cleanup := setupMaildirAPI(t)
	defer cleanup()

	// Store test messages
	msg1 := newTestHTTPMessage("api-msg-1", "sender@test.com", "recv@test.com", "Hello", "World")
	msg2 := newTestHTTPMessage("api-msg-2", "sender@test.com", "recv@test.com", "Foo", "Bar")
	maildir.Store(msg1)
	maildir.Store(msg2)

	req := httptest.NewRequest("GET", "/api/v1/messages", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected status 200, got %d; body: %s", w.Code, w.Body.String())
	}

	var messages data.Messages
	err := json.Unmarshal(w.Body.Bytes(), &messages)
	if err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("Expected 2 messages, got %d", len(messages))
	}
}

func TestAPIv1DownloadWithMaildir(t *testing.T) {
	_, router, maildir, cleanup := setupMaildirAPI(t)
	defer cleanup()

	msg := newTestHTTPMessage("dl-msg-1", "sender@test.com", "recv@test.com", "Download Test", "This is the body")
	maildir.Store(msg)

	req := httptest.NewRequest("GET", "/api/v1/messages/dl-msg-1/download", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected status 200, got %d; body: %s", w.Code, w.Body.String())
	}

	contentType := w.Header().Get("Content-Type")
	if contentType != "message/rfc822" {
		t.Fatalf("Expected Content-Type message/rfc822, got %s", contentType)
	}

	body := w.Body.String()
	if len(body) == 0 {
		t.Fatal("Download returned empty body")
	}
}

func TestAPIv1DownloadWithMaildirAfterDelete(t *testing.T) {
	_, router, maildir, cleanup := setupMaildirAPI(t)
	defer cleanup()

	msg := newTestHTTPMessage("del-msg-1", "sender@test.com", "recv@test.com", "Delete Test", "Body")
	maildir.Store(msg)

	// Delete the message
	maildir.DeleteOne("del-msg-1")

	req := httptest.NewRequest("GET", "/api/v1/messages/del-msg-1/download", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	// Should return 500 since message no longer exists
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("Expected status 500 after delete, got %d", w.Code)
	}
}

func TestAPIv1MessagesWithMaildirAfterDelete(t *testing.T) {
	_, router, maildir, cleanup := setupMaildirAPI(t)
	defer cleanup()

	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	// Store 3 messages
	for i, id := range []string{"d1", "d2", "d3"} {
		msg := newTestHTTPMessage(id, "sender@test.com", "recv@test.com", "Subject "+id, "Body "+id)
		maildir.Store(msg)
		// Set distinct mtimes for ordering
		os.Chtimes(filepath.Join(maildir.Path, id), base.Add(time.Duration(i)*time.Hour), base.Add(time.Duration(i)*time.Hour))
	}

	// Delete one message
	maildir.DeleteOne("d2")

	req := httptest.NewRequest("GET", "/api/v1/messages", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected status 200, got %d", w.Code)
	}

	var messages data.Messages
	err := json.Unmarshal(w.Body.Bytes(), &messages)
	if err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("Expected 2 messages after delete, got %d", len(messages))
	}
}

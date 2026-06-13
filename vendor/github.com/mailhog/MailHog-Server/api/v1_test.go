package api

import (
	"encoding/base64"
	"encoding/json"
	"io/ioutil"
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

// setupTestAPI creates an APIv1 instance with InMemory storage and returns
// a test HTTP server, the storage backend, and a cleanup function.
func setupTestAPI(t *testing.T) (*httptest.Server, *storage.InMemory, func()) {
	t.Helper()

	mem := storage.CreateInMemory()
	conf := &config.Config{
		Storage:      mem,
		WebPath:      "",
		CORSOrigin:   "",
		OutgoingSMTP: make(map[string]*config.OutgoingSMTP),
		MessageChan:  make(chan *data.Message),
		Hostname:     "test.mailhog",
	}

	router := pat.New()
	createAPIv1(conf, router)

	ts := httptest.NewServer(router)
	cleanup := func() {
		ts.Close()
	}

	return ts, mem, cleanup
}

// storeTestMessage stores a plain text message and returns its ID.
func storeTestMessage(t *testing.T, mem *storage.InMemory) string {
	t.Helper()
	msg := &data.Message{
		ID:   "test-message-001",
		From: &data.Path{Mailbox: "sender", Domain: "example.com"},
		To:   []*data.Path{{Mailbox: "recipient", Domain: "example.com"}},
		Content: &data.Content{
			Headers: map[string][]string{
				"From":         {"sender@example.com"},
				"To":           {"recipient@example.com"},
				"Subject":      {"Test Message"},
				"Content-Type": {"text/plain"},
			},
			Body: "Hello, this is a plain text test message.",
			Size: 45,
		},
		Created: time.Now(),
	}
	id, err := mem.Store(msg)
	if err != nil {
		t.Fatalf("Failed to store test message: %s", err)
	}
	return id
}

// storeMIMEMessage stores a MIME multipart message with a base64 attachment
// and returns its ID.
func storeMIMEMessage(t *testing.T, mem *storage.InMemory) string {
	t.Helper()

	// Create a base64-encoded "attachment" body
	attachmentContent := "This is the content of the test attachment file."
	base64Body := base64.StdEncoding.EncodeToString([]byte(attachmentContent))

	// Build MIME parts
	textPart := &data.Content{
		Headers: map[string][]string{
			"Content-Type": {"text/plain; charset=utf-8"},
		},
		Body: "This is the plain text part of a multipart message.",
	}

	attachmentPart := &data.Content{
		Headers: map[string][]string{
			"Content-Type":              {"application/octet-stream; name=\"test.txt\""},
			"Content-Disposition":       {"attachment; filename=\"test.txt\""},
			"Content-Transfer-Encoding": {"base64"},
		},
		Body: base64Body,
	}

	mimeBody := &data.MIMEBody{
		Parts: []*data.Content{textPart, attachmentPart},
	}

	msg := &data.Message{
		ID:   "test-mime-message-001",
		From: &data.Path{Mailbox: "sender", Domain: "example.com"},
		To:   []*data.Path{{Mailbox: "recipient", Domain: "example.com"}},
		Content: &data.Content{
			Headers: map[string][]string{
				"From":         {"sender@example.com"},
				"To":           {"recipient@example.com"},
				"Subject":      {"MIME Test Message"},
				"Content-Type": {"multipart/mixed; boundary=\"test-boundary-123\""},
			},
			Body: "This is a MIME message.",
			Size: 100,
			MIME: mimeBody,
		},
		Created: time.Now(),
		MIME:    mimeBody,
	}

	id, err := mem.Store(msg)
	if err != nil {
		t.Fatalf("Failed to store MIME test message: %s", err)
	}
	return id
}

// --- Message Detail Tests ---

func TestMessageNotFound(t *testing.T) {
	ts, _, cleanup := setupTestAPI(t)
	defer cleanup()

	resp, err := http.Get(ts.URL + "/api/v1/messages/nonexistent-id")
	if err != nil {
		t.Fatalf("Request failed: %s", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 404 {
		t.Errorf("Expected 404 for non-existent message, got %d", resp.StatusCode)
	}
}

func TestMessageDeleted(t *testing.T) {
	ts, mem, cleanup := setupTestAPI(t)
	defer cleanup()

	id := storeTestMessage(t, mem)

	// Delete the message
	err := mem.DeleteOne(id)
	if err != nil {
		t.Fatalf("Failed to delete message: %s", err)
	}

	resp, err := http.Get(ts.URL + "/api/v1/messages/" + id)
	if err != nil {
		t.Fatalf("Request failed: %s", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 404 {
		t.Errorf("Expected 404 for deleted message, got %d", resp.StatusCode)
	}
}

func TestMessageValid(t *testing.T) {
	ts, mem, cleanup := setupTestAPI(t)
	defer cleanup()

	id := storeTestMessage(t, mem)

	resp, err := http.Get(ts.URL + "/api/v1/messages/" + id)
	if err != nil {
		t.Fatalf("Request failed: %s", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("Expected 200 for valid message, got %d", resp.StatusCode)
	}

	body, _ := ioutil.ReadAll(resp.Body)
	var msg data.Message
	if err := json.Unmarshal(body, &msg); err != nil {
		t.Errorf("Response body is not valid JSON: %s", err)
	}
}

// --- Raw Download Tests ---

func TestDownloadNotFound(t *testing.T) {
	ts, _, cleanup := setupTestAPI(t)
	defer cleanup()

	resp, err := http.Get(ts.URL + "/api/v1/messages/nonexistent-id/download")
	if err != nil {
		t.Fatalf("Request failed: %s", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 404 {
		t.Errorf("Expected 404 for non-existent message download, got %d", resp.StatusCode)
	}
}

func TestDownloadDeleted(t *testing.T) {
	ts, mem, cleanup := setupTestAPI(t)
	defer cleanup()

	id := storeTestMessage(t, mem)
	mem.DeleteOne(id)

	resp, err := http.Get(ts.URL + "/api/v1/messages/" + id + "/download")
	if err != nil {
		t.Fatalf("Request failed: %s", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 404 {
		t.Errorf("Expected 404 for deleted message download, got %d", resp.StatusCode)
	}
}

func TestDownloadValid(t *testing.T) {
	ts, mem, cleanup := setupTestAPI(t)
	defer cleanup()

	id := storeTestMessage(t, mem)

	resp, err := http.Get(ts.URL + "/api/v1/messages/" + id + "/download")
	if err != nil {
		t.Fatalf("Request failed: %s", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("Expected 200 for valid download, got %d", resp.StatusCode)
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType != "message/rfc822" {
		t.Errorf("Expected Content-Type 'message/rfc822', got '%s'", contentType)
	}

	body, _ := ioutil.ReadAll(resp.Body)
	bodyStr := string(body)
	if !strings.Contains(bodyStr, "Subject: Test Message") {
		t.Errorf("Download body missing expected header, got: %s", bodyStr)
	}
	if !strings.Contains(bodyStr, "Hello, this is a plain text test message.") {
		t.Errorf("Download body missing expected body text, got: %s", bodyStr)
	}
}

// --- MIME Part Download Tests ---

func TestDownloadPartMessageNotFound(t *testing.T) {
	ts, _, cleanup := setupTestAPI(t)
	defer cleanup()

	resp, err := http.Get(ts.URL + "/api/v1/messages/nonexistent-id/mime/part/0/download")
	if err != nil {
		t.Fatalf("Request failed: %s", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 404 {
		t.Errorf("Expected 404 for non-existent message in part download, got %d", resp.StatusCode)
	}
}

func TestDownloadPartDeletedMessage(t *testing.T) {
	ts, mem, cleanup := setupTestAPI(t)
	defer cleanup()

	id := storeMIMEMessage(t, mem)
	mem.DeleteOne(id)

	resp, err := http.Get(ts.URL + "/api/v1/messages/" + id + "/mime/part/0/download")
	if err != nil {
		t.Fatalf("Request failed: %s", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 404 {
		t.Errorf("Expected 404 for deleted message part download, got %d", resp.StatusCode)
	}
}

func TestDownloadPartNonMIMEMMessage(t *testing.T) {
	ts, mem, cleanup := setupTestAPI(t)
	defer cleanup()

	id := storeTestMessage(t, mem) // plain text, no MIME parts

	resp, err := http.Get(ts.URL + "/api/v1/messages/" + id + "/mime/part/0/download")
	if err != nil {
		t.Fatalf("Request failed: %s", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 400 {
		t.Errorf("Expected 400 for non-MIME message part download, got %d", resp.StatusCode)
	}
}

func TestDownloadPartOutOfBounds(t *testing.T) {
	ts, mem, cleanup := setupTestAPI(t)
	defer cleanup()

	id := storeMIMEMessage(t, mem) // has 2 parts (index 0 and 1)

	resp, err := http.Get(ts.URL + "/api/v1/messages/" + id + "/mime/part/99/download")
	if err != nil {
		t.Fatalf("Request failed: %s", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 400 {
		t.Errorf("Expected 400 for out-of-bounds part index, got %d", resp.StatusCode)
	}
}

func TestDownloadPartNegativeIndex(t *testing.T) {
	ts, mem, cleanup := setupTestAPI(t)
	defer cleanup()

	id := storeMIMEMessage(t, mem)

	resp, err := http.Get(ts.URL + "/api/v1/messages/" + id + "/mime/part/-1/download")
	if err != nil {
		t.Fatalf("Request failed: %s", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 400 {
		t.Errorf("Expected 400 for negative part index, got %d", resp.StatusCode)
	}
}

func TestDownloadPartInvalidIndex(t *testing.T) {
	ts, mem, cleanup := setupTestAPI(t)
	defer cleanup()

	id := storeMIMEMessage(t, mem)

	resp, err := http.Get(ts.URL + "/api/v1/messages/" + id + "/mime/part/abc/download")
	if err != nil {
		t.Fatalf("Request failed: %s", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 400 {
		t.Errorf("Expected 400 for non-numeric part index, got %d", resp.StatusCode)
	}
}

func TestDownloadPartBase64Attachment(t *testing.T) {
	ts, mem, cleanup := setupTestAPI(t)
	defer cleanup()

	id := storeMIMEMessage(t, mem)

	// Download the second part (index 1) which is the base64 attachment
	resp, err := http.Get(ts.URL + "/api/v1/messages/" + id + "/mime/part/1/download")
	if err != nil {
		t.Fatalf("Request failed: %s", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("Expected 200 for valid base64 part download, got %d", resp.StatusCode)
	}

	body, _ := ioutil.ReadAll(resp.Body)
	expected := "This is the content of the test attachment file."
	if string(body) != expected {
		t.Errorf("Expected decoded base64 body '%s', got '%s'", expected, string(body))
	}

	// Verify Content-Disposition header is set from the part
	cd := resp.Header.Get("Content-Disposition")
	if !strings.Contains(cd, "attachment") {
		t.Errorf("Expected Content-Disposition to contain 'attachment', got '%s'", cd)
	}
}

func TestDownloadPartTextPart(t *testing.T) {
	ts, mem, cleanup := setupTestAPI(t)
	defer cleanup()

	id := storeMIMEMessage(t, mem)

	// Download the first part (index 0) which is the text part
	resp, err := http.Get(ts.URL + "/api/v1/messages/" + id + "/mime/part/0/download")
	if err != nil {
		t.Fatalf("Request failed: %s", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("Expected 200 for valid text part download, got %d", resp.StatusCode)
	}

	body, _ := ioutil.ReadAll(resp.Body)
	expected := "This is the plain text part of a multipart message."
	if string(body) != expected {
		t.Errorf("Expected body '%s', got '%s'", expected, string(body))
	}
}

// --- Messages List Tests ---

func TestMessagesListEmpty(t *testing.T) {
	ts, _, cleanup := setupTestAPI(t)
	defer cleanup()

	resp, err := http.Get(ts.URL + "/api/v1/messages")
	if err != nil {
		t.Fatalf("Request failed: %s", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("Expected 200 for empty message list, got %d", resp.StatusCode)
	}
}

func TestMessagesListWithData(t *testing.T) {
	ts, mem, cleanup := setupTestAPI(t)
	defer cleanup()

	storeTestMessage(t, mem)

	resp, err := http.Get(ts.URL + "/api/v1/messages")
	if err != nil {
		t.Fatalf("Request failed: %s", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("Expected 200 for message list, got %d", resp.StatusCode)
	}

	body, _ := ioutil.ReadAll(resp.Body)
	var msgs data.Messages
	if err := json.Unmarshal(body, &msgs); err != nil {
		t.Errorf("Response body is not valid JSON: %s", err)
	}
	if len(msgs) != 1 {
		t.Errorf("Expected 1 message in list, got %d", len(msgs))
	}
}

// --- Delete Tests ---

func TestDeleteOne(t *testing.T) {
	ts, mem, cleanup := setupTestAPI(t)
	defer cleanup()

	id := storeTestMessage(t, mem)

	req, _ := http.NewRequest("DELETE", ts.URL+"/api/v1/messages/"+id, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Request failed: %s", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("Expected 200 for delete, got %d", resp.StatusCode)
	}

	// Verify message is actually deleted
	msg, _ := mem.Load(id)
	if msg != nil {
		t.Error("Message should have been deleted but was still found")
	}
}

func TestDeleteAll(t *testing.T) {
	ts, mem, cleanup := setupTestAPI(t)
	defer cleanup()

	storeTestMessage(t, mem)

	req, _ := http.NewRequest("DELETE", ts.URL+"/api/v1/messages", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Request failed: %s", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("Expected 200 for delete all, got %d", resp.StatusCode)
	}

	if mem.Count() != 0 {
		t.Errorf("Expected 0 messages after delete all, got %d", mem.Count())
	}
}

// --- Stability Test: ensure server doesn't crash under rapid bad requests ---

func TestServerStabilityUnderBadRequest(t *testing.T) {
	ts, _, cleanup := setupTestAPI(t)
	defer cleanup()

	// Send a burst of requests for non-existent resources
	// The server should handle all of them without crashing
	endpoints := []string{
		"/api/v1/messages/nonexistent",
		"/api/v1/messages/nonexistent/download",
		"/api/v1/messages/nonexistent/mime/part/0/download",
		"/api/v1/messages/nonexistent/mime/part/abc/download",
		"/api/v1/messages/nonexistent/mime/part/999/download",
	}

	for i := 0; i < 5; i++ {
		for _, ep := range endpoints {
			resp, err := http.Get(ts.URL + ep)
			if err != nil {
				t.Fatalf("Server crashed or connection lost on request %s: %s", ep, err)
			}
			resp.Body.Close()
			// We expect 404 or 400, never a connection error
			if resp.StatusCode < 400 || resp.StatusCode >= 600 {
				t.Errorf("Unexpected status %d for %s", resp.StatusCode, ep)
			}
		}
	}

	// Verify server is still responsive after the burst
	resp, err := http.Get(ts.URL + "/api/v1/messages")
	if err != nil {
		t.Fatalf("Server unresponsive after bad request burst: %s", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("Server not healthy after burst, got status %d", resp.StatusCode)
	}
}

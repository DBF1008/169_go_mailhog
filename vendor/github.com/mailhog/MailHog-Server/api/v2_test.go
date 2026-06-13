package api

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/pat"
	"github.com/mailhog/MailHog-Server/config"
	"github.com/mailhog/data"
	"github.com/mailhog/storage"
)

// setupTestAPI creates an APIv2 with in-memory storage and a configured router.
// Returns the router (for serving HTTP requests in tests) and the storage backend.
func setupTestAPI(t *testing.T) (*pat.Router, *storage.InMemory) {
	t.Helper()
	memStore := storage.CreateInMemory()
	conf := &config.Config{
		Storage:      memStore,
		Hostname:     "test.example",
		OutgoingSMTP: make(map[string]*config.OutgoingSMTP),
		WebPath:      "",
		CORSOrigin:   "",
	}
	router := pat.New()
	createAPIv2(conf, router)
	return router, memStore
}

// setupTestAPIWithCORS is like setupTestAPI but with CORS enabled.
func setupTestAPIWithCORS(t *testing.T) (*pat.Router, *storage.InMemory) {
	t.Helper()
	memStore := storage.CreateInMemory()
	conf := &config.Config{
		Storage:      memStore,
		Hostname:     "test.example",
		OutgoingSMTP: make(map[string]*config.OutgoingSMTP),
		WebPath:      "",
		CORSOrigin:   "*",
	}
	router := pat.New()
	createAPIv2(conf, router)
	return router, memStore
}

// storeTestMessage builds a simple plain-text message and stores it directly.
func storeTestMessage(t *testing.T, store *storage.InMemory, id, from, to, subject, body string) *data.Message {
	t.Helper()
	msg := &data.Message{
		ID:   data.MessageID(id),
		From: data.PathFromString(from),
		To:   []*data.Path{data.PathFromString(to)},
		Content: &data.Content{
			Headers: map[string][]string{
				"From":         {from},
				"To":           {to},
				"Subject":      {subject},
				"Content-Type": {"text/plain"},
			},
			Body: body,
			Size: len(body),
		},
	}
	store.Messages = append(store.Messages, msg)
	store.MessageIDIndex[id] = len(store.Messages) - 1
	return msg
}

// storeMultipartMessage builds a message with two MIME parts (text + base64 binary).
func storeMultipartMessage(t *testing.T, store *storage.InMemory, id string) *data.Message {
	t.Helper()
	boundary := "----=_Part_Test_Boundary"
	body := "--" + boundary + "\r\n" +
		"Content-Type: text/plain\r\n\r\n" +
		"Plain text part\r\n" +
		"--" + boundary + "\r\n" +
		"Content-Type: application/octet-stream\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"Content-Disposition: attachment; filename=\"test.bin\"\r\n\r\n" +
		"SGVsbG8gV29ybGQ=\r\n" +
		"--" + boundary + "--\r\n"

	msg := &data.Message{
		ID:   data.MessageID(id),
		From: data.PathFromString("sender@example.com"),
		To:   []*data.Path{data.PathFromString("recipient@example.com")},
		Content: &data.Content{
			Headers: map[string][]string{
				"From":         {"sender@example.com"},
				"To":           {"recipient@example.com"},
				"Subject":      {"Multipart Test"},
				"Content-Type": {"multipart/mixed; boundary=\"" + boundary + "\""},
			},
			Body: body,
			Size: len(body),
		},
		MIME: &data.MIMEBody{
			Parts: []*data.Content{
				{
					Headers: map[string][]string{
						"Content-Type": {"text/plain"},
					},
					Body: "Plain text part",
				},
				{
					Headers: map[string][]string{
						"Content-Type":              {"application/octet-stream"},
						"Content-Transfer-Encoding": {"base64"},
						"Content-Disposition":       {"attachment; filename=\"test.bin\""},
					},
					Body: "SGVsbG8gV29ybGQ=",
				},
			},
		},
	}
	store.Messages = append(store.Messages, msg)
	store.MessageIDIndex[id] = len(store.Messages) - 1
	return msg
}

// startMockSMTP starts a minimal SMTP server on a random localhost port.
// It accepts one connection, speaks enough SMTP for smtp.SendMail to succeed,
// then closes. Returns the port string and a channel that receives the raw DATA.
func startMockSMTP(t *testing.T) (port string, done chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start mock SMTP: %v", err)
	}
	_, port, _ = net.SplitHostPort(ln.Addr().String())
	done = make(chan string, 1)

	go func() {
		defer ln.Close()
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		w := bufio.NewWriter(conn)
		r := bufio.NewReader(conn)

		// Greeting
		fmt.Fprintf(w, "220 mock.smtp.test ESMTP\r\n")
		w.Flush()

		var dataBuf strings.Builder
		inData := false

		for {
			line, err := r.ReadString('\n')
			if err != nil {
				break
			}
			line = strings.TrimRight(line, "\r\n")

			if inData {
				if line == "." {
					inData = false
					fmt.Fprintf(w, "250 OK\r\n")
					w.Flush()
					done <- dataBuf.String()
				} else {
					dataBuf.WriteString(line + "\r\n")
				}
				continue
			}

			cmd := strings.ToUpper(line)
			switch {
			case strings.HasPrefix(cmd, "EHLO") || strings.HasPrefix(cmd, "HELO"):
				fmt.Fprintf(w, "250-mock.smtp.test\r\n250-PIPELINING\r\n250 OK\r\n")
			case strings.HasPrefix(cmd, "MAIL FROM"):
				fmt.Fprintf(w, "250 OK\r\n")
			case strings.HasPrefix(cmd, "RCPT TO"):
				fmt.Fprintf(w, "250 OK\r\n")
			case strings.HasPrefix(cmd, "DATA"):
				fmt.Fprintf(w, "354 Start mail input\r\n")
				inData = true
			case strings.HasPrefix(cmd, "QUIT"):
				fmt.Fprintf(w, "221 Bye\r\n")
				w.Flush()
				return
			default:
				fmt.Fprintf(w, "250 OK\r\n")
			}
			w.Flush()
		}
	}()

	return port, done
}

// ---------- Message Detail Tests ----------

func TestV2GetMessage(t *testing.T) {
	router, store := setupTestAPI(t)
	storeTestMessage(t, store, "msg-001", "alice@sender.com", "bob@receiver.com", "Hello", "Body text")

	req := httptest.NewRequest("GET", "/api/v2/messages/msg-001", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	ct := rr.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("expected Content-Type application/json, got %s", ct)
	}

	var msg data.Message
	if err := json.Unmarshal(rr.Body.Bytes(), &msg); err != nil {
		t.Fatalf("failed to decode response JSON: %v", err)
	}
	if string(msg.ID) != "msg-001" {
		t.Errorf("expected message ID 'msg-001', got '%s'", msg.ID)
	}
	if msg.From.Mailbox != "alice" {
		t.Errorf("expected From mailbox 'alice', got '%s'", msg.From.Mailbox)
	}
}

func TestV2GetMessageNotFound(t *testing.T) {
	router, _ := setupTestAPI(t)

	req := httptest.NewRequest("GET", "/api/v2/messages/nonexistent", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
}

// ---------- Delete Single Tests ----------

func TestV2DeleteOne(t *testing.T) {
	router, store := setupTestAPI(t)
	storeTestMessage(t, store, "msg-del", "a@b.com", "c@d.com", "Subject", "Body")

	if store.Count() != 1 {
		t.Fatalf("expected 1 message before delete, got %d", store.Count())
	}

	req := httptest.NewRequest("DELETE", "/api/v2/messages/msg-del", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if store.Count() != 0 {
		t.Errorf("expected 0 messages after delete, got %d", store.Count())
	}
}

// ---------- Delete All Tests ----------

func TestV2DeleteAll(t *testing.T) {
	router, store := setupTestAPI(t)
	storeTestMessage(t, store, "m1", "a@b.com", "c@d.com", "S1", "B1")
	storeTestMessage(t, store, "m2", "a@b.com", "c@d.com", "S2", "B2")
	storeTestMessage(t, store, "m3", "a@b.com", "c@d.com", "S3", "B3")

	if store.Count() != 3 {
		t.Fatalf("expected 3 messages before delete, got %d", store.Count())
	}

	req := httptest.NewRequest("DELETE", "/api/v2/messages", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if store.Count() != 0 {
		t.Errorf("expected 0 messages after delete all, got %d", store.Count())
	}
}

// ---------- Delete-then-Read (404) Test ----------

func TestV2DeleteThenReadReturns404(t *testing.T) {
	router, store := setupTestAPI(t)
	storeTestMessage(t, store, "msg-gone", "a@b.com", "c@d.com", "Gone", "Body")

	// Delete first
	delReq := httptest.NewRequest("DELETE", "/api/v2/messages/msg-gone", nil)
	delRR := httptest.NewRecorder()
	router.ServeHTTP(delRR, delReq)
	if delRR.Code != http.StatusOK {
		t.Fatalf("delete expected 200, got %d", delRR.Code)
	}

	// Subsequent GET should return 404
	getReq := httptest.NewRequest("GET", "/api/v2/messages/msg-gone", nil)
	getRR := httptest.NewRecorder()
	router.ServeHTTP(getRR, getReq)
	if getRR.Code != http.StatusNotFound {
		t.Fatalf("expected 404 after delete, got %d", getRR.Code)
	}

	// Download should also return 404
	dlReq := httptest.NewRequest("GET", "/api/v2/messages/msg-gone/download", nil)
	dlRR := httptest.NewRecorder()
	router.ServeHTTP(dlRR, dlReq)
	if dlRR.Code != http.StatusNotFound {
		t.Fatalf("expected 404 on download after delete, got %d", dlRR.Code)
	}
}

// ---------- Download Raw .eml Tests ----------

func TestV2DownloadMessage(t *testing.T) {
	router, store := setupTestAPI(t)
	storeTestMessage(t, store, "msg-dl", "sender@test.com", "recv@test.com", "DL Test", "Email body here")

	req := httptest.NewRequest("GET", "/api/v2/messages/msg-dl/download", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	ct := rr.Header().Get("Content-Type")
	if ct != "message/rfc822" {
		t.Errorf("expected Content-Type message/rfc822, got %s", ct)
	}

	disp := rr.Header().Get("Content-Disposition")
	if !strings.Contains(disp, "msg-dl.eml") {
		t.Errorf("expected Content-Disposition to contain 'msg-dl.eml', got %s", disp)
	}

	body := rr.Body.String()
	if !strings.Contains(body, "Subject: DL Test") {
		t.Error("downloaded .eml should contain Subject header")
	}
	if !strings.Contains(body, "Email body here") {
		t.Error("downloaded .eml should contain the email body")
	}
	if !strings.Contains(body, "From: sender@test.com") {
		t.Error("downloaded .eml should contain From header")
	}
}

func TestV2DownloadMessageNotFound(t *testing.T) {
	router, _ := setupTestAPI(t)

	req := httptest.NewRequest("GET", "/api/v2/messages/no-such-id/download", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
}

// ---------- MIME Part Download Tests ----------

func TestV2DownloadMIMEPartText(t *testing.T) {
	router, store := setupTestAPI(t)
	storeMultipartMessage(t, store, "msg-mime")

	req := httptest.NewRequest("GET", "/api/v2/messages/msg-mime/mime/part/0/download", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	body := rr.Body.String()
	if !strings.Contains(body, "Plain text part") {
		t.Errorf("part 0 should contain 'Plain text part', got: %s", body)
	}

	ct := rr.Header().Get("Content-Type")
	if ct != "text/plain" {
		t.Errorf("expected Content-Type text/plain for part 0, got %s", ct)
	}
}

func TestV2DownloadMIMEPartBase64(t *testing.T) {
	router, store := setupTestAPI(t)
	storeMultipartMessage(t, store, "msg-mime-b64")

	req := httptest.NewRequest("GET", "/api/v2/messages/msg-mime-b64/mime/part/1/download", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	// base64 "SGVsbG8gV29ybGQ=" decodes to "Hello World"
	decoded := rr.Body.String()
	if decoded != "Hello World" {
		t.Errorf("expected decoded body 'Hello World', got '%s'", decoded)
	}

	disp := rr.Header().Get("Content-Disposition")
	if !strings.Contains(disp, "attachment") {
		t.Errorf("expected Content-Disposition attachment, got %s", disp)
	}
}

func TestV2DownloadMIMEPartInvalidIndex(t *testing.T) {
	router, store := setupTestAPI(t)
	storeMultipartMessage(t, store, "msg-mime-idx")

	req := httptest.NewRequest("GET", "/api/v2/messages/msg-mime-idx/mime/part/99/download", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for out-of-range part index, got %d", rr.Code)
	}
}

func TestV2DownloadMIMEPartNonMIME(t *testing.T) {
	router, store := setupTestAPI(t)
	storeTestMessage(t, store, "msg-nomime", "a@b.com", "c@d.com", "No MIME", "Just plain text")

	req := httptest.NewRequest("GET", "/api/v2/messages/msg-nomime/mime/part/0/download", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for non-MIME message, got %d", rr.Code)
	}
}

func TestV2DownloadMIMEPartMessageNotFound(t *testing.T) {
	router, _ := setupTestAPI(t)

	req := httptest.NewRequest("GET", "/api/v2/messages/missing/mime/part/0/download", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
}

// ---------- Release Tests ----------

func TestV2ReleaseMessageNotFound(t *testing.T) {
	router, _ := setupTestAPI(t)

	body := `{"email":"test@example.com","host":"localhost","port":"25"}`
	req := httptest.NewRequest("POST", "/api/v2/messages/nonexistent/release", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
}

func TestV2ReleaseInvalidBody(t *testing.T) {
	router, store := setupTestAPI(t)
	storeTestMessage(t, store, "msg-rel-bad", "a@b.com", "c@d.com", "Rel", "Body")

	req := httptest.NewRequest("POST", "/api/v2/messages/msg-rel-bad/release", strings.NewReader("not-json"))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid JSON body, got %d", rr.Code)
	}
}

func TestV2ReleaseUnknownServerName(t *testing.T) {
	router, store := setupTestAPI(t)
	storeTestMessage(t, store, "msg-rel-name", "a@b.com", "c@d.com", "Rel", "Body")

	body := `{"name":"nonexistent-server"}`
	req := httptest.NewRequest("POST", "/api/v2/messages/msg-rel-name/release", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown server name, got %d", rr.Code)
	}
}

func TestV2ReleaseInvalidAuthMechanism(t *testing.T) {
	router, store := setupTestAPI(t)
	storeTestMessage(t, store, "msg-rel-auth", "a@b.com", "c@d.com", "Rel", "Body")

	body := `{"email":"test@example.com","host":"localhost","port":"25","username":"user","password":"pass","mechanism":"INVALID"}`
	req := httptest.NewRequest("POST", "/api/v2/messages/msg-rel-auth/release", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid auth mechanism, got %d", rr.Code)
	}
}

func TestV2ReleaseSMTPFailure(t *testing.T) {
	router, store := setupTestAPI(t)
	storeTestMessage(t, store, "msg-rel-fail", "a@b.com", "c@d.com", "Rel", "Body")

	// Find a port that is definitely not listening
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to get free port: %v", err)
	}
	_, closedPort, _ := net.SplitHostPort(ln.Addr().String())
	ln.Close() // close immediately so the port refuses connections

	body := fmt.Sprintf(`{"email":"test@example.com","host":"127.0.0.1","port":"%s"}`, closedPort)
	req := httptest.NewRequest("POST", "/api/v2/messages/msg-rel-fail/release", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 for SMTP connection failure, got %d", rr.Code)
	}
}

func TestV2ReleaseSaveDuplicateServer(t *testing.T) {
	router, store := setupTestAPIWithConfig(t, map[string]*config.OutgoingSMTP{
		"myserver": {Name: "myserver", Host: "smtp.example.com", Port: "587"},
	})
	storeTestMessage(t, store, "msg-rel-dup", "a@b.com", "c@d.com", "Rel", "Body")

	body := `{"name":"myserver","save":true,"email":"t@e.com","host":"h","port":"25"}`
	req := httptest.NewRequest("POST", "/api/v2/messages/msg-rel-dup/release", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for duplicate server save, got %d", rr.Code)
	}
}

func TestV2ReleaseSuccess(t *testing.T) {
	// Start a mock SMTP server
	smtpPort, dataCh := startMockSMTP(t)

	router, store := setupTestAPI(t)
	storeTestMessage(t, store, "msg-rel-ok", "a@b.com", "c@d.com", "Release Test", "Released body")

	body := fmt.Sprintf(`{"email":"recipient@example.com","host":"127.0.0.1","port":"%s"}`, smtpPort)
	req := httptest.NewRequest("POST", "/api/v2/messages/msg-rel-ok/release", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for successful release, got %d; body: %s", rr.Code, rr.Body.String())
	}

	// Verify mock SMTP received the message data
	received := <-dataCh
	if !strings.Contains(received, "Released body") {
		t.Errorf("mock SMTP should have received message body, got: %s", received)
	}
	if !strings.Contains(received, "Subject: Release Test") {
		t.Errorf("mock SMTP should have received Subject header, got: %s", received)
	}
}

func TestV2ReleaseWithNamedServer(t *testing.T) {
	smtpPort, dataCh := startMockSMTP(t)

	router, store := setupTestAPIWithConfig(t, map[string]*config.OutgoingSMTP{
		"testserver": {Name: "testserver", Host: "127.0.0.1", Port: smtpPort, Email: "named@example.com"},
	})
	storeTestMessage(t, store, "msg-rel-named", "a@b.com", "c@d.com", "Named Release", "Named body")

	body := `{"name":"testserver"}`
	req := httptest.NewRequest("POST", "/api/v2/messages/msg-rel-named/release", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for named server release, got %d; body: %s", rr.Code, rr.Body.String())
	}

	received := <-dataCh
	if !strings.Contains(received, "Named body") {
		t.Errorf("mock SMTP should have received named message body, got: %s", received)
	}
}

// ---------- CORS Test ----------

func TestV2CORSHeadersOnNewEndpoints(t *testing.T) {
	router, store := setupTestAPIWithCORS(t)
	storeTestMessage(t, store, "msg-cors", "a@b.com", "c@d.com", "CORS", "Body")

	// OPTIONS on messages/{id}
	req := httptest.NewRequest("OPTIONS", "/api/v2/messages/msg-cors", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	origin := rr.Header().Get("Access-Control-Allow-Origin")
	if origin != "*" {
		t.Errorf("expected CORS Allow-Origin '*', got '%s'", origin)
	}
	methods := rr.Header().Get("Access-Control-Allow-Methods")
	if !strings.Contains(methods, "DELETE") {
		t.Errorf("expected CORS Allow-Methods to include DELETE, got '%s'", methods)
	}
}

// ---------- Helper with pre-configured OutgoingSMTP ----------

func setupTestAPIWithConfig(t *testing.T, outgoingSMTP map[string]*config.OutgoingSMTP) (*pat.Router, *storage.InMemory) {
	t.Helper()
	memStore := storage.CreateInMemory()
	conf := &config.Config{
		Storage:      memStore,
		Hostname:     "test.example",
		OutgoingSMTP: outgoingSMTP,
		WebPath:      "",
		CORSOrigin:   "",
	}
	router := pat.New()
	createAPIv2(conf, router)
	return router, memStore
}

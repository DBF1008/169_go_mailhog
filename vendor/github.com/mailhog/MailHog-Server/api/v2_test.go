package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/smtp"
	"strings"
	"testing"

	"github.com/gorilla/pat"
	"github.com/mailhog/MailHog-Server/config"
	"github.com/mailhog/data"
	"github.com/mailhog/storage"
)

// newTestAPIv2 wires the v2 API onto a real pat router backed by in-memory
// storage, so tests exercise routing and path-parameter extraction end to end.
func newTestAPIv2(t *testing.T) (*pat.Router, *config.Config) {
	t.Helper()
	conf := &config.Config{
		Hostname:     "mailhog.example",
		Storage:      storage.CreateInMemory(),
		OutgoingSMTP: make(map[string]*config.OutgoingSMTP),
		MessageChan:  make(chan *data.Message),
	}
	r := pat.New()
	createAPIv2(conf, r)
	return r, conf
}

// do issues a request through the router and returns the recorded response.
func do(r *pat.Router, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// capturedSend records the arguments of a stubbed SMTP send.
type capturedSend struct {
	called bool
	addr   string
	from   string
	to     []string
	msg    []byte
	err    error
}

// stubSend replaces the package smtpSendMail seam with a recorder for the
// duration of the test, so releases never touch the network.
func stubSend(t *testing.T) *capturedSend {
	t.Helper()
	orig := smtpSendMail
	sent := &capturedSend{}
	smtpSendMail = func(addr string, a smtp.Auth, from string, to []string, msg []byte) error {
		sent.called = true
		sent.addr = addr
		sent.from = from
		sent.to = to
		sent.msg = msg
		return sent.err
	}
	t.Cleanup(func() { smtpSendMail = orig })
	return sent
}

// storeMessage stores a message and returns its storage id.
func storeMessage(t *testing.T, conf *config.Config) string {
	t.Helper()
	msg := &data.Message{
		ID: data.MessageID("test-msg-1"),
		Content: &data.Content{
			Headers: map[string][]string{
				"From":    {"sender@example"},
				"To":      {"original@example"},
				"Subject": {"hello"},
			},
			Body: "test body",
		},
	}
	id, err := conf.Storage.Store(msg)
	if err != nil {
		t.Fatalf("store message: %s", err)
	}
	return id
}

// TestCreateOutgoingSMTPValidation covers template validation: a well-formed
// template is created (201) while invalid ones are rejected (400).
func TestCreateOutgoingSMTPValidation(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int
	}{
		{"valid", `{"Name":"prod","Host":"smtp.example","Port":"25"}`, http.StatusCreated},
		{"valid with plain auth", `{"Name":"prod","Host":"smtp.example","Port":"25","Username":"u","Password":"p","Mechanism":"PLAIN"}`, http.StatusCreated},
		{"missing name", `{"Host":"smtp.example","Port":"25"}`, http.StatusBadRequest},
		{"missing host", `{"Name":"prod","Port":"25"}`, http.StatusBadRequest},
		{"missing port", `{"Name":"prod","Host":"smtp.example"}`, http.StatusBadRequest},
		{"bad mechanism", `{"Name":"prod","Host":"smtp.example","Port":"25","Mechanism":"OAUTH"}`, http.StatusBadRequest},
		{"creds without mechanism", `{"Name":"prod","Host":"smtp.example","Port":"25","Username":"u","Password":"p"}`, http.StatusBadRequest},
		{"malformed json", `{not json`, http.StatusBadRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, _ := newTestAPIv2(t)
			rec := do(r, "POST", "/api/v2/outgoing-smtp", c.body)
			if rec.Code != c.want {
				t.Fatalf("got status %d, want %d (body: %s)", rec.Code, c.want, rec.Body.String())
			}
		})
	}
}

// TestCreateOutgoingSMTPDuplicateName covers duplicate-name handling: a second
// create with the same name is rejected with 409 and does not add a second entry.
func TestCreateOutgoingSMTPDuplicateName(t *testing.T) {
	r, conf := newTestAPIv2(t)
	body := `{"Name":"prod","Host":"smtp.example","Port":"25"}`

	if rec := do(r, "POST", "/api/v2/outgoing-smtp", body); rec.Code != http.StatusCreated {
		t.Fatalf("first create: got %d, want 201 (%s)", rec.Code, rec.Body.String())
	}
	if rec := do(r, "POST", "/api/v2/outgoing-smtp", body); rec.Code != http.StatusConflict {
		t.Fatalf("duplicate create: got %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
	if n := len(conf.OutgoingSMTP); n != 1 {
		t.Fatalf("expected exactly 1 server after duplicate create, got %d", n)
	}
}

// TestOutgoingSMTPCRUDRoundTrip exercises the full create/get/update/delete cycle.
func TestOutgoingSMTPCRUDRoundTrip(t *testing.T) {
	r, conf := newTestAPIv2(t)

	if rec := do(r, "POST", "/api/v2/outgoing-smtp", `{"Name":"prod","Host":"smtp.example","Port":"25","Email":"ops@example"}`); rec.Code != http.StatusCreated {
		t.Fatalf("create: got %d (%s)", rec.Code, rec.Body.String())
	}

	rec := do(r, "GET", "/api/v2/outgoing-smtp/prod", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get: got %d", rec.Code)
	}
	var got config.OutgoingSMTP
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("get decode: %s", err)
	}
	if got.Host != "smtp.example" || got.Email != "ops@example" {
		t.Fatalf("get returned unexpected server: %+v", got)
	}

	// Update changes host/port; the path name stays authoritative.
	if rec := do(r, "PUT", "/api/v2/outgoing-smtp/prod", `{"Name":"ignored","Host":"smtp2.example","Port":"587","Email":"ops2@example"}`); rec.Code != http.StatusOK {
		t.Fatalf("update: got %d (%s)", rec.Code, rec.Body.String())
	}
	if s := conf.OutgoingSMTP["prod"]; s == nil || s.Host != "smtp2.example" || s.Port != "587" || s.Name != "prod" {
		t.Fatalf("update did not persist correctly: %+v", conf.OutgoingSMTP["prod"])
	}

	if rec := do(r, "PUT", "/api/v2/outgoing-smtp/missing", `{"Host":"h","Port":"25"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("update missing: got %d, want 404", rec.Code)
	}

	if rec := do(r, "DELETE", "/api/v2/outgoing-smtp/prod", ""); rec.Code != http.StatusOK {
		t.Fatalf("delete: got %d", rec.Code)
	}
	if _, ok := conf.OutgoingSMTP["prod"]; ok {
		t.Fatalf("server still present after delete")
	}
	if rec := do(r, "GET", "/api/v2/outgoing-smtp/prod", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("get after delete: got %d, want 404", rec.Code)
	}
	if rec := do(r, "DELETE", "/api/v2/outgoing-smtp/prod", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("delete again: got %d, want 404", rec.Code)
	}
}

// TestCreateOutgoingSMTPNilMap ensures create initialises the map when the
// config was built without one.
func TestCreateOutgoingSMTPNilMap(t *testing.T) {
	conf := &config.Config{
		Hostname:    "mailhog.example",
		Storage:     storage.CreateInMemory(),
		MessageChan: make(chan *data.Message),
		// OutgoingSMTP intentionally left nil
	}
	r := pat.New()
	createAPIv2(conf, r)

	if rec := do(r, "POST", "/api/v2/outgoing-smtp", `{"Name":"prod","Host":"smtp.example","Port":"25"}`); rec.Code != http.StatusCreated {
		t.Fatalf("create with nil map: got %d (%s)", rec.Code, rec.Body.String())
	}
	if conf.OutgoingSMTP["prod"] == nil {
		t.Fatalf("expected server to be stored after lazy map init")
	}
}

// TestReleaseReuseTemplateAndOverride verifies a message can be released by
// reusing a stored template, and that the recipient can be explicitly overridden.
func TestReleaseReuseTemplateAndOverride(t *testing.T) {
	r, conf := newTestAPIv2(t)
	id := storeMessage(t, conf)
	conf.OutgoingSMTP["prod"] = &config.OutgoingSMTP{Name: "prod", Host: "smtp.example", Port: "25", Email: "default@example"}

	// Reuse the template with no override: recipient comes from the template.
	sent := stubSend(t)
	rec := do(r, "POST", "/api/v2/messages/"+id+"/release", `{"Name":"prod"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("release: got %d (%s)", rec.Code, rec.Body.String())
	}
	if !sent.called {
		t.Fatalf("expected send to be called")
	}
	if len(sent.to) != 1 || sent.to[0] != "default@example" {
		t.Fatalf("expected recipient default@example, got %v", sent.to)
	}
	if sent.addr != "smtp.example:25" {
		t.Fatalf("expected addr smtp.example:25, got %s", sent.addr)
	}
	if sent.from != "nobody@mailhog.example" {
		t.Fatalf("expected from nobody@mailhog.example, got %s", sent.from)
	}
	if !bytes.Contains(sent.msg, []byte("Subject: hello")) || !bytes.Contains(sent.msg, []byte("test body")) {
		t.Fatalf("released message missing expected content: %q", sent.msg)
	}

	// Reuse the template but override the recipient.
	sent2 := stubSend(t)
	rec = do(r, "POST", "/api/v2/messages/"+id+"/release", `{"Name":"prod","Email":"override@example"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("release override: got %d (%s)", rec.Code, rec.Body.String())
	}
	if len(sent2.to) != 1 || sent2.to[0] != "override@example" {
		t.Fatalf("expected recipient override@example, got %v", sent2.to)
	}
}

// TestReleaseValidation covers release-time validation: a template name is
// required, and a recipient must be resolvable from the template or override.
func TestReleaseValidation(t *testing.T) {
	r, conf := newTestAPIv2(t)
	id := storeMessage(t, conf)

	// Missing template name -> 400, no send.
	sent := stubSend(t)
	if rec := do(r, "POST", "/api/v2/messages/"+id+"/release", `{}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing name: got %d, want 400", rec.Code)
	}
	if sent.called {
		t.Fatalf("send must not be called when name is missing")
	}

	// Template without an email and no override -> 400, no send.
	conf.OutgoingSMTP["noemail"] = &config.OutgoingSMTP{Name: "noemail", Host: "h", Port: "25"}
	sent2 := stubSend(t)
	if rec := do(r, "POST", "/api/v2/messages/"+id+"/release", `{"Name":"noemail"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing recipient: got %d, want 400", rec.Code)
	}
	if sent2.called {
		t.Fatalf("send must not be called when recipient is missing")
	}

	// Unknown message id (template fine, recipient overridden) -> 404.
	conf.OutgoingSMTP["prod"] = &config.OutgoingSMTP{Name: "prod", Host: "h", Port: "25", Email: "ops@example"}
	if rec := do(r, "POST", "/api/v2/messages/does-not-exist/release", `{"Name":"prod"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown message: got %d, want 404", rec.Code)
	}
}

// TestReleaseDeletedTemplateUnusable verifies that once a template is deleted it
// can no longer be used to release a message (404), and that no send occurs.
func TestReleaseDeletedTemplateUnusable(t *testing.T) {
	r, conf := newTestAPIv2(t)
	id := storeMessage(t, conf)

	if rec := do(r, "POST", "/api/v2/outgoing-smtp", `{"Name":"prod","Host":"smtp.example","Port":"25","Email":"ops@example"}`); rec.Code != http.StatusCreated {
		t.Fatalf("create: got %d (%s)", rec.Code, rec.Body.String())
	}

	// Release succeeds while the template exists.
	sent := stubSend(t)
	if rec := do(r, "POST", "/api/v2/messages/"+id+"/release", `{"Name":"prod"}`); rec.Code != http.StatusOK {
		t.Fatalf("release before delete: got %d (%s)", rec.Code, rec.Body.String())
	}
	if !sent.called {
		t.Fatalf("expected send before delete")
	}

	if rec := do(r, "DELETE", "/api/v2/outgoing-smtp/prod", ""); rec.Code != http.StatusOK {
		t.Fatalf("delete: got %d", rec.Code)
	}

	// Releasing via the now-deleted template fails and must not send.
	sent2 := stubSend(t)
	rec := do(r, "POST", "/api/v2/messages/"+id+"/release", `{"Name":"prod"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("release after delete: got %d, want 404 (%s)", rec.Code, rec.Body.String())
	}
	if sent2.called {
		t.Fatalf("send must not be called for a deleted template")
	}
}

package api

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/mailhog/MailHog-Server/config"
	"github.com/mailhog/data"
	"github.com/mailhog/storage"
)

// newTestAPIv1 builds an APIv1 backed by the given storage without starting the
// event-stream goroutine that createAPIv1 would.
func newTestAPIv1(s storage.Storage) *APIv1 {
	return &APIv1{
		config: &config.Config{Storage: s},
	}
}

// reqWithVars builds a request the way gorilla/pat hands it to a handler: path
// variables are exposed through the query string with a ":" prefix.
func reqWithVars(vars map[string]string) *http.Request {
	v := url.Values{}
	for k, val := range vars {
		v.Set(":"+k, val)
	}
	return httptest.NewRequest(http.MethodGet, "/?"+v.Encode(), nil)
}

// seededStorage returns an in-memory store containing a multipart message
// ("valid") with a base64 part and a plain part, plus a non-multipart message
// ("plain").
func seededStorage(t *testing.T) *storage.InMemory {
	t.Helper()
	mem := storage.CreateInMemory()

	multipart := &data.Message{
		ID: data.MessageID("valid"),
		Content: &data.Content{
			Headers: map[string][]string{
				"Subject":      {"Hi"},
				"Content-Type": {"multipart/mixed; boundary=x"},
			},
			Body: "raw message body",
		},
		MIME: &data.MIMEBody{
			Parts: []*data.Content{
				{
					Headers: map[string][]string{
						"Content-Transfer-Encoding": {"base64"},
						"Content-Type":              {"text/plain"},
					},
					Body: base64.StdEncoding.EncodeToString([]byte("hello attachment")),
				},
				{
					Headers: map[string][]string{
						"Content-Type": {"text/plain"},
					},
					Body: "plain part body",
				},
			},
		},
	}
	if _, err := mem.Store(multipart); err != nil {
		t.Fatalf("seeding multipart message: %s", err)
	}

	plain := &data.Message{
		ID: data.MessageID("plain"),
		Content: &data.Content{
			Headers: map[string][]string{"Subject": {"plain"}},
			Body:    "just text",
		},
		MIME: nil, // non-multipart message: no MIME parts
	}
	if _, err := mem.Store(plain); err != nil {
		t.Fatalf("seeding plain message: %s", err)
	}

	return mem
}

// TestHandlersStableStatus is the core regression test. Requesting a deleted or
// unknown message, a MIME part of a non-multipart message, or an out-of-range /
// non-numeric part index must return a stable HTTP status instead of panicking
// (which previously aborted the connection) or returning 500. A panic inside a
// handler is not recovered by httptest and therefore fails the test outright.
func TestHandlersStableStatus(t *testing.T) {
	mem := seededStorage(t)

	// A genuinely deleted message: stored, then removed. Load now returns
	// (nil, nil) for it, the case that used to panic / 500.
	deleted := &data.Message{
		ID:      data.MessageID("deleted"),
		Content: &data.Content{Headers: map[string][]string{}, Body: ""},
	}
	if _, err := mem.Store(deleted); err != nil {
		t.Fatalf("seeding deleted message: %s", err)
	}
	if err := mem.DeleteOne("deleted"); err != nil {
		t.Fatalf("deleting message: %s", err)
	}

	api := newTestAPIv1(mem)

	cases := []struct {
		name    string
		handler http.HandlerFunc
		vars    map[string]string
		want    int
	}{
		// message details
		{"message_valid", api.message, map[string]string{"id": "valid"}, http.StatusOK},
		{"message_unknown_id", api.message, map[string]string{"id": "ghost"}, http.StatusNotFound},
		{"message_deleted", api.message, map[string]string{"id": "deleted"}, http.StatusNotFound},

		// raw (.eml) download
		{"download_valid", api.download, map[string]string{"id": "valid"}, http.StatusOK},
		{"download_non_mime", api.download, map[string]string{"id": "plain"}, http.StatusOK},
		{"download_unknown_id", api.download, map[string]string{"id": "ghost"}, http.StatusNotFound},
		{"download_deleted", api.download, map[string]string{"id": "deleted"}, http.StatusNotFound},

		// MIME part download
		{"part_valid", api.download_part, map[string]string{"id": "valid", "part": "0"}, http.StatusOK},
		{"part_unknown_id", api.download_part, map[string]string{"id": "ghost", "part": "0"}, http.StatusNotFound},
		{"part_deleted", api.download_part, map[string]string{"id": "deleted", "part": "0"}, http.StatusNotFound},
		{"part_non_mime_message", api.download_part, map[string]string{"id": "plain", "part": "0"}, http.StatusNotFound},
		{"part_out_of_range", api.download_part, map[string]string{"id": "valid", "part": "5"}, http.StatusNotFound},
		{"part_negative_index", api.download_part, map[string]string{"id": "valid", "part": "-1"}, http.StatusNotFound},
		{"part_non_numeric", api.download_part, map[string]string{"id": "valid", "part": "abc"}, http.StatusNotFound},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			tc.handler(w, reqWithVars(tc.vars)) // a pre-fix panic fails the test here
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d (body: %q)", w.Code, tc.want, w.Body.String())
			}
		})
	}
}

// TestDownloadPartBase64 verifies that a normal base64-encoded attachment is
// still decoded correctly; the fix must not regress this path.
func TestDownloadPartBase64(t *testing.T) {
	api := newTestAPIv1(seededStorage(t))

	w := httptest.NewRecorder()
	api.download_part(w, reqWithVars(map[string]string{"id": "valid", "part": "0"}))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %q)", w.Code, w.Body.String())
	}
	if got := w.Body.String(); got != "hello attachment" {
		t.Fatalf("decoded body = %q, want %q", got, "hello attachment")
	}
}

// TestDownloadPartPlain verifies a part without a base64 transfer encoding is
// returned verbatim.
func TestDownloadPartPlain(t *testing.T) {
	api := newTestAPIv1(seededStorage(t))

	w := httptest.NewRecorder()
	api.download_part(w, reqWithVars(map[string]string{"id": "valid", "part": "1"}))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %q)", w.Code, w.Body.String())
	}
	if got := w.Body.String(); got != "plain part body" {
		t.Fatalf("body = %q, want %q", got, "plain part body")
	}
}

// TestMessageAndDownloadHappyPath verifies the success paths still emit the
// expected content after the nil-guarding changes.
func TestMessageAndDownloadHappyPath(t *testing.T) {
	api := newTestAPIv1(seededStorage(t))

	t.Run("message", func(t *testing.T) {
		w := httptest.NewRecorder()
		api.message(w, reqWithVars(map[string]string{"id": "valid"}))
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}
		if !strings.Contains(w.Body.String(), "valid") {
			t.Fatalf("message body %q does not contain the id", w.Body.String())
		}
	})

	t.Run("download", func(t *testing.T) {
		w := httptest.NewRecorder()
		api.download(w, reqWithVars(map[string]string{"id": "valid"}))
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}
		body := w.Body.String()
		if !strings.Contains(body, "Subject: Hi") {
			t.Fatalf("download body missing headers: %q", body)
		}
		if !strings.Contains(body, "raw message body") {
			t.Fatalf("download body missing message body: %q", body)
		}
	})
}

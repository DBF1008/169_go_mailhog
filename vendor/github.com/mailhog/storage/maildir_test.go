package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mailhog/data"
)

// storeMessage builds a message from the given envelope/body and stores it in
// the maildir, returning its storage ID (the on-disk file name).
func storeMessage(t *testing.T, m *Maildir, from, to, body string) string {
	t.Helper()
	smtp := &data.SMTPMessage{
		Helo: "localhost",
		From: from,
		To:   []string{to},
		Data: "Subject: Test\r\n\r\n" + body,
	}
	id, err := m.Store(smtp.Parse("mailhog.example"))
	if err != nil {
		t.Fatalf("Store failed: %s", err)
	}
	return id
}

// Empty directory: listing a maildir with no messages must succeed and return
// an empty (non-nil) result rather than erroring.
func TestMaildirListEmpty(t *testing.T) {
	m := CreateMaildir(t.TempDir())

	msgs, err := m.List(0, 50)
	if err != nil {
		t.Fatalf("List on empty maildir returned error: %s", err)
	}
	if msgs == nil {
		t.Fatal("List returned nil messages")
	}
	if len(*msgs) != 0 {
		t.Fatalf("expected 0 messages, got %d", len(*msgs))
	}
	if c := m.Count(); c != 0 {
		t.Fatalf("expected count 0, got %d", c)
	}
}

// List read: stored messages must all come back from List.
func TestMaildirListReturnsStoredMessages(t *testing.T) {
	m := CreateMaildir(t.TempDir())
	id1 := storeMessage(t, m, "alice@example.com", "bob@example.com", "Hello Bob")
	id2 := storeMessage(t, m, "carol@example.com", "dave@example.com", "Hello Dave")

	msgs, err := m.List(0, 50)
	if err != nil {
		t.Fatalf("List returned error: %s", err)
	}
	if len(*msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(*msgs))
	}

	got := map[string]bool{}
	for _, mm := range *msgs {
		got[string(mm.ID)] = true
	}
	if !got[id1] || !got[id2] {
		t.Fatalf("List is missing stored messages: ids=%v (want %s, %s)", got, id1, id2)
	}
	if c := m.Count(); c != 2 {
		t.Fatalf("expected count 2, got %d", c)
	}
}

// List must be newest-first and honor start/limit, matching the in-memory and
// MongoDB backends.
func TestMaildirListOrderingAndPaging(t *testing.T) {
	dir := t.TempDir()
	m := CreateMaildir(dir)
	idOld := storeMessage(t, m, "alice@example.com", "bob@example.com", "older")
	idNew := storeMessage(t, m, "carol@example.com", "dave@example.com", "newer")

	// Force deterministic modification times so ordering is not subject to
	// filesystem timestamp resolution.
	older := time.Unix(1000, 0)
	newer := time.Unix(2000, 0)
	if err := os.Chtimes(filepath.Join(dir, idOld), older, older); err != nil {
		t.Fatalf("Chtimes: %s", err)
	}
	if err := os.Chtimes(filepath.Join(dir, idNew), newer, newer); err != nil {
		t.Fatalf("Chtimes: %s", err)
	}

	all, err := m.List(0, 50)
	if err != nil {
		t.Fatalf("List: %s", err)
	}
	if len(*all) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(*all))
	}
	if string((*all)[0].ID) != idNew || string((*all)[1].ID) != idOld {
		t.Fatalf("expected newest-first ordering [%s, %s], got [%s, %s]",
			idNew, idOld, (*all)[0].ID, (*all)[1].ID)
	}

	first, err := m.List(0, 1)
	if err != nil {
		t.Fatalf("List: %s", err)
	}
	if len(*first) != 1 || string((*first)[0].ID) != idNew {
		t.Fatalf("limit=1 should return only the newest message, got %d items", len(*first))
	}

	second, err := m.List(1, 1)
	if err != nil {
		t.Fatalf("List: %s", err)
	}
	if len(*second) != 1 || string((*second)[0].ID) != idOld {
		t.Fatalf("start=1,limit=1 should return the older message, got %d items", len(*second))
	}
}

// A stray subdirectory (or any non-message entry) must not cause the whole
// listing to fail — this is the regression behind list/UI 500s in maildir mode.
func TestMaildirListIgnoresSubdirectories(t *testing.T) {
	dir := t.TempDir()
	m := CreateMaildir(dir)
	id := storeMessage(t, m, "alice@example.com", "bob@example.com", "Hello Bob")

	if err := os.Mkdir(filepath.Join(dir, "cur"), 0770); err != nil {
		t.Fatalf("Mkdir: %s", err)
	}

	msgs, err := m.List(0, 50)
	if err != nil {
		t.Fatalf("List returned error with a subdirectory present: %s", err)
	}
	if len(*msgs) != 1 || string((*msgs)[0].ID) != id {
		t.Fatalf("expected only the stored message, got %d items", len(*msgs))
	}
}

// Single download: Load is the data path behind the raw download endpoint. An
// existing message must load with its content intact.
func TestMaildirLoadSingle(t *testing.T) {
	m := CreateMaildir(t.TempDir())
	id := storeMessage(t, m, "alice@example.com", "bob@example.com", "Hello Bob")

	msg, err := m.Load(id)
	if err != nil {
		t.Fatalf("Load returned error: %s", err)
	}
	if msg == nil {
		t.Fatal("Load returned nil for an existing message")
	}
	if string(msg.ID) != id {
		t.Fatalf("ID mismatch: got %s, want %s", msg.ID, id)
	}
	if msg.From.Mailbox != "alice" || msg.From.Domain != "example.com" {
		t.Fatalf("unexpected From: %+v", msg.From)
	}
	if !strings.Contains(msg.Content.Body, "Hello Bob") {
		t.Fatalf("message body missing content: %q", msg.Content.Body)
	}
}

// Delete then read: after deleting a message, listing must still succeed and
// exclude it, and loading the deleted ID must be graceful (nil, nil) rather
// than an error — matching the in-memory backend.
func TestMaildirDeleteThenRead(t *testing.T) {
	m := CreateMaildir(t.TempDir())
	id1 := storeMessage(t, m, "alice@example.com", "bob@example.com", "Hello Bob")
	id2 := storeMessage(t, m, "carol@example.com", "dave@example.com", "Hello Dave")

	if err := m.DeleteOne(id1); err != nil {
		t.Fatalf("DeleteOne: %s", err)
	}

	msgs, err := m.List(0, 50)
	if err != nil {
		t.Fatalf("List after delete returned error: %s", err)
	}
	if len(*msgs) != 1 || string((*msgs)[0].ID) != id2 {
		t.Fatalf("expected only the remaining message %s, got %d items", id2, len(*msgs))
	}
	if c := m.Count(); c != 1 {
		t.Fatalf("expected count 1 after delete, got %d", c)
	}

	gone, err := m.Load(id1)
	if err != nil {
		t.Fatalf("Load of deleted message returned error: %s", err)
	}
	if gone != nil {
		t.Fatalf("expected nil for deleted message, got %+v", gone)
	}

	kept, err := m.Load(id2)
	if err != nil {
		t.Fatalf("Load of remaining message returned error: %s", err)
	}
	if kept == nil {
		t.Fatal("remaining message could not be loaded after delete")
	}
}

package storage

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mailhog/data"
)

// newTestMessage creates a test data.Message with the given ID and content
func newTestMessage(id, from, to, subject, body string) *data.Message {
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

// createTempMaildir creates a Maildir in a temporary directory for testing
func createTempMaildir(t *testing.T) (*Maildir, func()) {
	t.Helper()
	dir, err := ioutil.TempDir("", "mailhog-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	m := CreateMaildir(dir)
	cleanup := func() {
		os.RemoveAll(dir)
	}
	return m, cleanup
}

// storeTestMessage stores a test message and sets its Created time via file mtime
func storeTestMessage(t *testing.T, m *Maildir, id, from, to, subject, body string, created time.Time) {
	t.Helper()
	msg := newTestMessage(id, from, to, subject, body)
	_, err := m.Store(msg)
	if err != nil {
		t.Fatalf("Failed to store message %s: %v", id, err)
	}
	// Set file modification time to control Created time in List()
	err = os.Chtimes(filepath.Join(m.Path, id), created, created)
	if err != nil {
		t.Fatalf("Failed to set mtime for message %s: %v", id, err)
	}
}

func TestMaildirListEmptyDirectory(t *testing.T) {
	m, cleanup := createTempMaildir(t)
	defer cleanup()

	messages, err := m.List(0, 100)
	if err != nil {
		t.Fatalf("List on empty directory returned error: %v", err)
	}
	if messages == nil {
		t.Fatal("List on empty directory returned nil messages")
	}
	if len(*messages) != 0 {
		t.Fatalf("Expected 0 messages, got %d", len(*messages))
	}
}

func TestMaildirCountEmptyDirectory(t *testing.T) {
	m, cleanup := createTempMaildir(t)
	defer cleanup()

	count := m.Count()
	if count != 0 {
		t.Fatalf("Expected count 0, got %d", count)
	}
}

func TestMaildirStoreAndLoad(t *testing.T) {
	m, cleanup := createTempMaildir(t)
	defer cleanup()

	msg := newTestMessage("test-msg-1", "sender@test.com", "recipient@test.com", "Test", "Hello World")
	id, err := m.Store(msg)
	if err != nil {
		t.Fatalf("Store failed: %v", err)
	}
	if id != "test-msg-1" {
		t.Fatalf("Expected ID 'test-msg-1', got '%s'", id)
	}

	loaded, err := m.Load("test-msg-1")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if loaded == nil {
		t.Fatal("Load returned nil message")
	}
	if string(loaded.ID) != "test-msg-1" {
		t.Fatalf("Expected loaded ID 'test-msg-1', got '%s'", loaded.ID)
	}
	if loaded.Content == nil {
		t.Fatal("Loaded message has nil Content")
	}
}

func TestMaildirListWithMessages(t *testing.T) {
	m, cleanup := createTempMaildir(t)
	defer cleanup()

	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	// Store 3 messages with different creation times
	storeTestMessage(t, m, "msg-1", "a@test.com", "b@test.com", "First", "Body 1", base)
	storeTestMessage(t, m, "msg-2", "a@test.com", "b@test.com", "Second", "Body 2", base.Add(1*time.Hour))
	storeTestMessage(t, m, "msg-3", "a@test.com", "b@test.com", "Third", "Body 3", base.Add(2*time.Hour))

	// Verify count
	if m.Count() != 3 {
		t.Fatalf("Expected count 3, got %d", m.Count())
	}

	// List all
	messages, err := m.List(0, 100)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if messages == nil {
		t.Fatal("List returned nil")
	}
	msgs := *messages
	if len(msgs) != 3 {
		t.Fatalf("Expected 3 messages, got %d", len(msgs))
	}

	// Verify newest-first order (msg-3 should be first)
	if string(msgs[0].ID) != "msg-3" {
		t.Fatalf("Expected first message ID 'msg-3', got '%s'", msgs[0].ID)
	}
	if string(msgs[1].ID) != "msg-2" {
		t.Fatalf("Expected second message ID 'msg-2', got '%s'", msgs[1].ID)
	}
	if string(msgs[2].ID) != "msg-1" {
		t.Fatalf("Expected third message ID 'msg-1', got '%s'", msgs[2].ID)
	}
}

func TestMaildirListPagination(t *testing.T) {
	m, cleanup := createTempMaildir(t)
	defer cleanup()

	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	// Store 5 messages
	for i := 0; i < 5; i++ {
		id := string(rune('A'+i)) + "-msg"
		storeTestMessage(t, m, id, "a@test.com", "b@test.com", "Subject "+id, "Body", base.Add(time.Duration(i)*time.Hour))
	}

	// Test limit=2, start=0 → should get the 2 newest (E-msg, D-msg)
	messages, err := m.List(0, 2)
	if err != nil {
		t.Fatalf("List(0,2) failed: %v", err)
	}
	msgs := *messages
	if len(msgs) != 2 {
		t.Fatalf("Expected 2 messages, got %d", len(msgs))
	}
	if string(msgs[0].ID) != "E-msg" {
		t.Fatalf("Expected first message 'E-msg', got '%s'", msgs[0].ID)
	}
	if string(msgs[1].ID) != "D-msg" {
		t.Fatalf("Expected second message 'D-msg', got '%s'", msgs[1].ID)
	}

	// Test limit=2, start=2 → should get C-msg, B-msg
	messages, err = m.List(2, 2)
	if err != nil {
		t.Fatalf("List(2,2) failed: %v", err)
	}
	msgs = *messages
	if len(msgs) != 2 {
		t.Fatalf("Expected 2 messages, got %d", len(msgs))
	}
	if string(msgs[0].ID) != "C-msg" {
		t.Fatalf("Expected first message 'C-msg', got '%s'", msgs[0].ID)
	}

	// Test limit=10, start=3 → should get B-msg, A-msg (only 2 remaining)
	messages, err = m.List(3, 10)
	if err != nil {
		t.Fatalf("List(3,10) failed: %v", err)
	}
	msgs = *messages
	if len(msgs) != 2 {
		t.Fatalf("Expected 2 messages, got %d", len(msgs))
	}

	// Test start beyond total → should return empty
	messages, err = m.List(10, 10)
	if err != nil {
		t.Fatalf("List(10,10) failed: %v", err)
	}
	msgs = *messages
	if len(msgs) != 0 {
		t.Fatalf("Expected 0 messages for out-of-range start, got %d", len(msgs))
	}
}

func TestMaildirDeleteOneThenList(t *testing.T) {
	m, cleanup := createTempMaildir(t)
	defer cleanup()

	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	storeTestMessage(t, m, "msg-1", "a@test.com", "b@test.com", "First", "Body 1", base)
	storeTestMessage(t, m, "msg-2", "a@test.com", "b@test.com", "Second", "Body 2", base.Add(1*time.Hour))
	storeTestMessage(t, m, "msg-3", "a@test.com", "b@test.com", "Third", "Body 3", base.Add(2*time.Hour))

	// Delete the middle message
	err := m.DeleteOne("msg-2")
	if err != nil {
		t.Fatalf("DeleteOne failed: %v", err)
	}

	// Count should be 2
	if m.Count() != 2 {
		t.Fatalf("Expected count 2 after delete, got %d", m.Count())
	}

	// List should return msg-3 and msg-1 in that order
	messages, err := m.List(0, 100)
	if err != nil {
		t.Fatalf("List after delete failed: %v", err)
	}
	msgs := *messages
	if len(msgs) != 2 {
		t.Fatalf("Expected 2 messages after delete, got %d", len(msgs))
	}
	if string(msgs[0].ID) != "msg-3" {
		t.Fatalf("Expected first message 'msg-3', got '%s'", msgs[0].ID)
	}
	if string(msgs[1].ID) != "msg-1" {
		t.Fatalf("Expected second message 'msg-1', got '%s'", msgs[1].ID)
	}

	// Load deleted message should fail
	_, err = m.Load("msg-2")
	if err == nil {
		t.Fatal("Load of deleted message should return error")
	}
}

func TestMaildirDeleteAll(t *testing.T) {
	m, cleanup := createTempMaildir(t)
	defer cleanup()

	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	storeTestMessage(t, m, "msg-1", "a@test.com", "b@test.com", "First", "Body 1", base)
	storeTestMessage(t, m, "msg-2", "a@test.com", "b@test.com", "Second", "Body 2", base.Add(1*time.Hour))

	err := m.DeleteAll()
	if err != nil {
		t.Fatalf("DeleteAll failed: %v", err)
	}

	if m.Count() != 0 {
		t.Fatalf("Expected count 0 after DeleteAll, got %d", m.Count())
	}

	messages, err := m.List(0, 100)
	if err != nil {
		t.Fatalf("List after DeleteAll failed: %v", err)
	}
	if len(*messages) != 0 {
		t.Fatalf("Expected 0 messages after DeleteAll, got %d", len(*messages))
	}
}

func TestMaildirLoadNonExistent(t *testing.T) {
	m, cleanup := createTempMaildir(t)
	defer cleanup()

	_, err := m.Load("nonexistent-id")
	if err == nil {
		t.Fatal("Load of non-existent message should return error")
	}
}

func TestMaildirDeleteNonExistent(t *testing.T) {
	m, cleanup := createTempMaildir(t)
	defer cleanup()

	err := m.DeleteOne("nonexistent-id")
	if err == nil {
		t.Fatal("DeleteOne of non-existent message should return error")
	}
}

// TestMaildirListMatchesInMemoryPagination verifies that Maildir pagination
// produces the same ordering as InMemory storage for the same message set.
func TestMaildirListMatchesInMemoryPagination(t *testing.T) {
	m, cleanup := createTempMaildir(t)
	defer cleanup()

	mem := CreateInMemory()

	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	// Store 5 messages in both backends
	ids := []string{"m1", "m2", "m3", "m4", "m5"}
	for i, id := range ids {
		msg := newTestMessage(id, "a@test.com", "b@test.com", "Subject "+id, "Body "+id)
		msg.Created = base.Add(time.Duration(i) * time.Hour)

		// Store in maildir with matching mtime
		storeTestMessage(t, m, id, "a@test.com", "b@test.com", "Subject "+id, "Body "+id, base.Add(time.Duration(i)*time.Hour))

		// Store in memory (messages stored in order, so m5 is last/newest)
		mem.Store(msg)
	}

	// Compare List(0, 3) from both backends
	maildirMsgs, err := m.List(0, 3)
	if err != nil {
		t.Fatalf("Maildir List(0,3) failed: %v", err)
	}
	memoryMsgs, err := mem.List(0, 3)
	if err != nil {
		t.Fatalf("InMemory List(0,3) failed: %v", err)
	}

	md := *maildirMsgs
	me := *memoryMsgs
	if len(md) != len(me) {
		t.Fatalf("Maildir returned %d messages, InMemory returned %d", len(md), len(me))
	}

	// Both should return newest first: m5, m4, m3
	for i := range md {
		if string(md[i].ID) != string(me[i].ID) {
			t.Fatalf("Order mismatch at index %d: Maildir=%s, InMemory=%s", i, md[i].ID, me[i].ID)
		}
	}

	// Compare List(2, 2) - should return m3, m2
	maildirMsgs, err = m.List(2, 2)
	if err != nil {
		t.Fatalf("Maildir List(2,2) failed: %v", err)
	}
	memoryMsgs, err = mem.List(2, 2)
	if err != nil {
		t.Fatalf("InMemory List(2,2) failed: %v", err)
	}

	md = *maildirMsgs
	me = *memoryMsgs
	if len(md) != len(me) {
		t.Fatalf("Maildir returned %d messages, InMemory returned %d for List(2,2)", len(md), len(me))
	}
	for i := range md {
		if string(md[i].ID) != string(me[i].ID) {
			t.Fatalf("Order mismatch at index %d for List(2,2): Maildir=%s, InMemory=%s", i, md[i].ID, me[i].ID)
		}
	}
}

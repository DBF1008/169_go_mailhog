package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/mailhog/data"
)

// These tests pin down the message query semantics that the v2 API depends on
// (newest-first ordering, windowed pagination, and a search total that is
// independent of the window) and assert that the in-process backends
// (InMemory and Maildir) produce consistent results. The MongoDB backend
// implements the same contract via server-side query operators and is not
// exercised here as it requires a running MongoDB instance.

// makeMsg builds a parsed message with a controllable ID, addresses, body and
// Created timestamp. A fixed Message-ID header is included so Parse does not
// inject a random one, keeping header-based matching deterministic.
func makeMsg(id, from string, to []string, body string, created time.Time) *data.Message {
	sm := &data.SMTPMessage{
		Helo: "localhost",
		From: from,
		To:   to,
		Data: "Message-ID: <" + id + ">\r\nSubject: " + id + "\r\n\r\n" + body,
	}
	m := sm.Parse("mailhog.example")
	m.ID = data.MessageID(id)
	m.Created = created
	return m
}

// corpus returns a fresh set of five messages whose Created timestamps strictly
// increase from msg-01 (oldest) to msg-05 (newest), so the expected
// newest-first order is msg-05, msg-04, msg-03, msg-02, msg-01.
func corpus() []*data.Message {
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	return []*data.Message{
		makeMsg("msg-01", "alice@example.com", []string{"bob@example.org"}, "hello world", base.Add(1*time.Minute)),
		makeMsg("msg-02", "alice@example.com", []string{"carol@example.org"}, "lunch at noon", base.Add(2*time.Minute)),
		makeMsg("msg-03", "dave@example.net", []string{"bob@example.org"}, "report attached", base.Add(3*time.Minute)),
		makeMsg("msg-04", "erin@example.com", []string{"bob@example.org"}, "hello again", base.Add(4*time.Minute)),
		makeMsg("msg-05", "alice@example.com", []string{"frank@example.io"}, "final notice", base.Add(5*time.Minute)),
	}
}

// seed stores the given messages in the backend. For the maildir backend it
// also aligns each file's modification time with the message's Created, because
// maildir derives Created from the file mtime; this gives deterministic
// ordering that matches the in-memory backend.
func seed(t *testing.T, store Storage, msgs ...*data.Message) {
	t.Helper()
	for _, m := range msgs {
		if _, err := store.Store(m); err != nil {
			t.Fatalf("Store(%s): %v", m.ID, err)
		}
		if md, ok := store.(*Maildir); ok {
			p := filepath.Join(md.Path, string(m.ID))
			if err := os.Chtimes(p, m.Created, m.Created); err != nil {
				t.Fatalf("Chtimes(%s): %v", p, err)
			}
		}
	}
}

// ids extracts the message IDs from a result set, in order.
func ids(messages *data.Messages) []string {
	out := make([]string, 0, len(*messages))
	for _, m := range *messages {
		out = append(out, string(m.ID))
	}
	return out
}

func assertIDs(t *testing.T, got *data.Messages, want []string) {
	t.Helper()
	if g := ids(got); !reflect.DeepEqual(g, want) {
		t.Fatalf("ids = %v, want %v", g, want)
	}
}

type backend struct {
	name string
	make func(t *testing.T) Storage
}

// inProcessBackends returns the backends that can be exercised without external
// services. Each scenario runs against all of them so their behaviour stays
// consistent.
func inProcessBackends() []backend {
	return []backend{
		{"memory", func(t *testing.T) Storage { return CreateInMemory() }},
		{"maildir", func(t *testing.T) Storage { return CreateMaildir(t.TempDir()) }},
	}
}

func TestListNewestFirstAndPaging(t *testing.T) {
	for _, b := range inProcessBackends() {
		t.Run(b.name, func(t *testing.T) {
			store := b.make(t)
			seed(t, store, corpus()...)

			full, err := store.List(0, 50)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			assertIDs(t, full, []string{"msg-05", "msg-04", "msg-03", "msg-02", "msg-01"})

			if c := store.Count(); c != 5 {
				t.Fatalf("Count = %d, want 5", c)
			}

			// Page through the list in windows of two.
			page1, _ := store.List(0, 2)
			assertIDs(t, page1, []string{"msg-05", "msg-04"})

			page2, _ := store.List(2, 2)
			assertIDs(t, page2, []string{"msg-03", "msg-02"})

			page3, _ := store.List(4, 2)
			assertIDs(t, page3, []string{"msg-01"})

			// A start at or beyond the end yields an empty page; the total
			// (Count) is unaffected.
			atEnd, _ := store.List(5, 2)
			assertIDs(t, atEnd, []string{})

			beyond, _ := store.List(99, 2)
			assertIDs(t, beyond, []string{})

			if c := store.Count(); c != 5 {
				t.Fatalf("Count after paging = %d, want 5", c)
			}
		})
	}
}

func TestSearchByKind(t *testing.T) {
	cases := []struct {
		name, kind, query string
		wantIDs           []string
		wantTotal         int
	}{
		{"from", "from", "alice", []string{"msg-05", "msg-02", "msg-01"}, 3},
		{"to", "to", "bob", []string{"msg-04", "msg-03", "msg-01"}, 3},
		{"containing", "containing", "hello", []string{"msg-04", "msg-01"}, 2},
	}
	for _, b := range inProcessBackends() {
		for _, c := range cases {
			t.Run(b.name+"/"+c.name, func(t *testing.T) {
				store := b.make(t)
				seed(t, store, corpus()...)

				got, total, err := store.Search(c.kind, c.query, 0, 50)
				if err != nil {
					t.Fatalf("Search: %v", err)
				}
				assertIDs(t, got, c.wantIDs)
				if total != c.wantTotal {
					t.Fatalf("total = %d, want %d", total, c.wantTotal)
				}
			})
		}
	}
}

func TestSearchCaseInsensitive(t *testing.T) {
	for _, b := range inProcessBackends() {
		t.Run(b.name, func(t *testing.T) {
			store := b.make(t)
			seed(t, store, corpus()...)

			got, total, _ := store.Search("from", "ALICE", 0, 50)
			assertIDs(t, got, []string{"msg-05", "msg-02", "msg-01"})
			if total != 3 {
				t.Fatalf("total = %d, want 3", total)
			}
		})
	}
}

// TestSearchTotalIndependentOfWindow guards the regression where a search total
// must report the full number of matches regardless of the requested window.
func TestSearchTotalIndependentOfWindow(t *testing.T) {
	for _, b := range inProcessBackends() {
		t.Run(b.name, func(t *testing.T) {
			store := b.make(t)
			seed(t, store, corpus()...)

			// Three messages are from alice; a limit of one must still report
			// a total of three.
			page, total, _ := store.Search("from", "alice", 0, 1)
			assertIDs(t, page, []string{"msg-05"})
			if total != 3 {
				t.Fatalf("total with limit 1 = %d, want 3", total)
			}

			page, total, _ = store.Search("from", "alice", 1, 1)
			assertIDs(t, page, []string{"msg-02"})
			if total != 3 {
				t.Fatalf("total at offset 1 = %d, want 3", total)
			}

			page, total, _ = store.Search("from", "alice", 2, 50)
			assertIDs(t, page, []string{"msg-01"})
			if total != 3 {
				t.Fatalf("total at offset 2 = %d, want 3", total)
			}

			// A window past the matches yields an empty page but the full total.
			page, total, _ = store.Search("from", "alice", 5, 50)
			assertIDs(t, page, []string{})
			if total != 3 {
				t.Fatalf("total past end = %d, want 3", total)
			}
		})
	}
}

func TestEmptyResults(t *testing.T) {
	for _, b := range inProcessBackends() {
		t.Run(b.name, func(t *testing.T) {
			store := b.make(t)

			// Listing an empty store yields a non-nil empty result and a zero count.
			list, err := store.List(0, 50)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			assertIDs(t, list, []string{})
			if []data.Message(*list) == nil {
				t.Fatal("List should return a non-nil slice for an empty store")
			}
			if c := store.Count(); c != 0 {
				t.Fatalf("Count = %d, want 0", c)
			}

			// A search with no matches on a populated store yields an empty
			// result and a zero total.
			seed(t, store, corpus()...)
			res, total, err := store.Search("to", "nobody@nowhere.invalid", 0, 50)
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			assertIDs(t, res, []string{})
			if []data.Message(*res) == nil {
				t.Fatal("Search should return a non-nil slice when there are no matches")
			}
			if total != 0 {
				t.Fatalf("total = %d, want 0", total)
			}
		})
	}
}

// TestBackendsProduceConsistentResults is the explicit statement of the goal:
// given identical input, the in-process backends return identical windows and
// totals for the same list and search queries.
func TestBackendsProduceConsistentResults(t *testing.T) {
	mem := CreateInMemory()
	mail := CreateMaildir(t.TempDir())
	seed(t, mem, corpus()...)
	seed(t, mail, corpus()...)

	listWindows := []struct{ start, limit int }{
		{0, 50}, {0, 2}, {2, 2}, {4, 2}, {5, 2}, {0, 0}, {99, 10},
	}
	for _, w := range listWindows {
		m, err := mem.List(w.start, w.limit)
		if err != nil {
			t.Fatalf("memory List: %v", err)
		}
		d, err := mail.List(w.start, w.limit)
		if err != nil {
			t.Fatalf("maildir List: %v", err)
		}
		if !reflect.DeepEqual(ids(m), ids(d)) {
			t.Errorf("List(%d,%d): memory %v != maildir %v", w.start, w.limit, ids(m), ids(d))
		}
	}

	if mem.Count() != mail.Count() {
		t.Errorf("Count: memory %d != maildir %d", mem.Count(), mail.Count())
	}

	searches := []struct {
		kind, query  string
		start, limit int
	}{
		{"from", "alice", 0, 50},
		{"from", "alice", 0, 1},
		{"from", "alice", 1, 2},
		{"to", "bob", 0, 50},
		{"containing", "hello", 0, 50},
		{"to", "nobody", 0, 50},
	}
	for _, s := range searches {
		m, tm, _ := mem.Search(s.kind, s.query, s.start, s.limit)
		d, td, _ := mail.Search(s.kind, s.query, s.start, s.limit)
		if !reflect.DeepEqual(ids(m), ids(d)) {
			t.Errorf("Search(%q,%q,%d,%d): memory %v != maildir %v", s.kind, s.query, s.start, s.limit, ids(m), ids(d))
		}
		if tm != td {
			t.Errorf("Search(%q,%q) total: memory %d != maildir %d", s.kind, s.query, tm, td)
		}
	}
}

func TestWindow(t *testing.T) {
	mk := func(n int) []data.Message {
		s := make([]data.Message, n)
		for i := range s {
			s[i].ID = data.MessageID(fmt.Sprintf("%d", i))
		}
		return s
	}
	cases := []struct {
		name            string
		n, start, limit int
		want            []string
	}{
		{"full", 5, 0, 50, []string{"0", "1", "2", "3", "4"}},
		{"first page", 5, 0, 2, []string{"0", "1"}},
		{"middle page", 5, 2, 2, []string{"2", "3"}},
		{"last partial page", 5, 4, 2, []string{"4"}},
		{"start at end", 5, 5, 2, []string{}},
		{"start past end", 5, 99, 2, []string{}},
		{"zero limit", 5, 1, 0, []string{}},
		{"negative limit", 5, 0, -3, []string{}},
		{"negative start", 5, -3, 2, []string{"0", "1"}},
		{"empty input", 0, 0, 50, []string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := window(mk(c.n), c.start, c.limit)
			if got == nil {
				t.Fatal("window returned nil")
			}
			gotIDs := make([]string, len(got))
			for i, m := range got {
				gotIDs[i] = string(m.ID)
			}
			if !reflect.DeepEqual(gotIDs, c.want) {
				t.Fatalf("window(n=%d, start=%d, limit=%d) = %v, want %v", c.n, c.start, c.limit, gotIDs, c.want)
			}
		})
	}

	// The returned page is a copy: mutating it must not affect the input.
	in := mk(3)
	page := window(in, 0, 3)
	page[0].ID = "mutated"
	if in[0].ID == "mutated" {
		t.Fatal("window must return a copy, not a view into the input")
	}
}

func TestSortByCreatedDesc(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	msgs := []data.Message{
		{ID: "a", Created: base.Add(2 * time.Hour)},
		{ID: "b", Created: base.Add(1 * time.Hour)},
		{ID: "c", Created: base.Add(3 * time.Hour)},
	}
	sortByCreatedDesc(msgs)

	got := []string{string(msgs[0].ID), string(msgs[1].ID), string(msgs[2].ID)}
	want := []string{"c", "a", "b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sorted = %v, want %v", got, want)
	}
}

func TestMatches(t *testing.T) {
	m := makeMsg("subj", "alice@example.com", []string{"bob@example.org"}, "hello world", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	cases := []struct {
		name, kind, query string
		want              bool
	}{
		{"from match", "from", "alice", true},
		{"from match case-insensitive", "from", "ALICE@EXAMPLE.COM", true},
		{"from no match", "from", "zzz", false},
		{"to match", "to", "bob", true},
		{"to no match", "to", "zzz", false},
		{"containing body", "containing", "hello", true},
		{"containing header value", "containing", "mailhog", true},
		{"containing no match", "containing", "zzz-not-present", false},
		{"unknown kind", "banana", "alice", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := matches(m, c.kind, c.query); got != c.want {
				t.Fatalf("matches(%q, %q) = %v, want %v", c.kind, c.query, got, c.want)
			}
		})
	}

	// A sparse message (nil From, empty headers) must not panic.
	sparse := &data.Message{Content: &data.Content{Headers: map[string][]string{}}}
	if matches(sparse, "from", "x") {
		t.Error("expected no from match on a sparse message")
	}
	if matches(sparse, "to", "x") {
		t.Error("expected no to match on a sparse message")
	}
	if matches(sparse, "containing", "x") {
		t.Error("expected no containing match on a sparse message")
	}
}

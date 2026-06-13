package storage

import (
	"sort"
	"strings"

	"github.com/mailhog/data"
)

// This file defines the shared listing, sorting, windowing and search-matching
// semantics used by the in-process storage backends (InMemory and Maildir), so
// that List, Search, pagination and total counts behave identically regardless
// of which of those backends is in use.
//
// The MongoDB backend implements the same contract using equivalent
// server-side query operators (Sort("-created") for ordering, Skip/Limit for
// the window and a separate Count for the total); see mongodb.go.
//
// The contract is:
//
//   - Messages are ordered newest-first, by their Created timestamp (the most
//     recently received message comes first).
//   - A "window" is the half-open page [start, start+limit) over that ordered
//     set of messages.
//   - A Search "total" is always the full number of matches, independent of the
//     window that was applied to them.

// sortByCreatedDesc orders messages newest-first by their Created timestamp.
//
// The sort is stable, so messages that share a timestamp keep their existing
// relative order.
func sortByCreatedDesc(messages []data.Message) {
	sort.SliceStable(messages, func(i, j int) bool {
		return messages[i].Created.After(messages[j].Created)
	})
}

// window returns the page [start, start+limit) of an already-ordered slice.
//
// It always returns a non-nil slice (so callers marshal an empty result to a
// JSON array rather than null) and never panics for out-of-range arguments:
//
//   - start is clamped to [0, len(messages)]; a start at or beyond the end
//     yields an empty page.
//   - limit <= 0 yields an empty page; a limit that runs past the end is
//     truncated to the end.
//
// The returned slice is a copy, so mutating it does not affect the input.
func window(messages []data.Message, start, limit int) []data.Message {
	n := len(messages)

	if start < 0 {
		start = 0
	}
	if start > n {
		start = n
	}

	if limit < 0 {
		limit = 0
	}

	end := n
	if start+limit < end {
		end = start + limit
	}

	page := make([]data.Message, end-start)
	copy(page, messages[start:end])
	return page
}

// matches reports whether a message matches the given search kind and query.
//
// The supported kinds are "to", "from" and "containing". Matching is
// case-insensitive and, mirroring what a user sees in the UI, checks both the
// parsed addresses and the raw message headers:
//
//   - "to":         each recipient address, then the "To" header.
//   - "from":       the sender address, then the "From" header.
//   - "containing": the message body, then every header value.
//
// Any unknown kind matches nothing.
func matches(m *data.Message, kind, query string) bool {
	query = strings.ToLower(query)

	switch kind {
	case "to":
		for _, to := range m.To {
			if to != nil && strings.Contains(strings.ToLower(to.Mailbox+"@"+to.Domain), query) {
				return true
			}
		}
		return headerContains(m, "To", query)
	case "from":
		if m.From != nil && strings.Contains(strings.ToLower(m.From.Mailbox+"@"+m.From.Domain), query) {
			return true
		}
		return headerContains(m, "From", query)
	case "containing":
		if m.Content != nil {
			if strings.Contains(strings.ToLower(m.Content.Body), query) {
				return true
			}
			for _, values := range m.Content.Headers {
				for _, v := range values {
					if strings.Contains(strings.ToLower(v), query) {
						return true
					}
				}
			}
		}
	}

	return false
}

// headerContains reports whether any value of the named header contains the
// (already lower-cased) query.
func headerContains(m *data.Message, name, query string) bool {
	if m.Content == nil {
		return false
	}
	for _, v := range m.Content.Headers[name] {
		if strings.Contains(strings.ToLower(v), query) {
			return true
		}
	}
	return false
}

package api

import (
	"net/http/httptest"
	"testing"
)

// TestGetStartLimit pins down the pagination parameter semantics shared by the
// v2 messages and search endpoints: a default limit of 50, a hard cap of 250,
// and start/limit that fall back to their defaults for zero, negative or
// unparseable values.
func TestGetStartLimit(t *testing.T) {
	cases := []struct {
		name      string
		query     string
		wantStart int
		wantLimit int
	}{
		{"defaults", "", 0, 50},
		{"explicit", "start=10&limit=20", 10, 20},
		{"limit capped at 250", "limit=1000", 0, 250},
		{"zero ignored", "start=0&limit=0", 0, 50},
		{"negative ignored", "start=-5&limit=-5", 0, 50},
		{"garbage ignored", "start=abc&limit=xyz", 0, 50},
	}

	a := &APIv2{}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/v2/messages?"+c.query, nil)
			start, limit := a.getStartLimit(nil, req)
			if start != c.wantStart || limit != c.wantLimit {
				t.Fatalf("getStartLimit(%q) = (%d, %d), want (%d, %d)", c.query, start, limit, c.wantStart, c.wantLimit)
			}
		})
	}
}

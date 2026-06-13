package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestConfigureLoadsOutgoingSMTPFile covers the startup path: when an outgoing
// SMTP JSON file is configured, Configure() loads its servers into the config so
// they are available (e.g. for release) from the moment the process starts.
func TestConfigureLoadsOutgoingSMTPFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "outgoing.json")
	content := `{
		"prod":   {"Name":"prod","Host":"smtp.example","Port":"587","Email":"ops@example","Mechanism":"PLAIN","Username":"u","Password":"p"},
		"backup": {"Name":"backup","Host":"smtp2.example","Port":"25"}
	}`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("write temp file: %s", err)
	}

	cfg.StorageType = "memory"
	cfg.OutgoingSMTPFile = path

	c := Configure()

	if c.OutgoingSMTP == nil {
		t.Fatalf("expected OutgoingSMTP to be loaded, got nil")
	}
	if n := len(c.OutgoingSMTP); n != 2 {
		t.Fatalf("expected 2 loaded servers, got %d", n)
	}

	prod, ok := c.OutgoingSMTP["prod"]
	if !ok {
		t.Fatalf("expected 'prod' server to be loaded")
	}
	if prod.Host != "smtp.example" || prod.Port != "587" || prod.Email != "ops@example" ||
		prod.Mechanism != "PLAIN" || prod.Username != "u" || prod.Password != "p" {
		t.Fatalf("loaded 'prod' server has unexpected fields: %+v", prod)
	}

	if _, ok := c.OutgoingSMTP["backup"]; !ok {
		t.Fatalf("expected 'backup' server to be loaded")
	}
}

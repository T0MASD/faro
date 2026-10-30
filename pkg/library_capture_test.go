package faro

import (
	"os"
	"path/filepath"
	"testing"
)

// A program that embeds faro to react to changes must not get a database it
// never asked for.
//
// It did: the SQLite capture defaulted to <output_dir>/logs/capture.db, so a
// library consumer that set output_dir for its own logs - as every example in
// this repository does - had every event written to a database by a process
// that never reads it. Found dogfooding faro as the watcher for a work inbox:
// FARO_SQLITE_FILE appeared in every test run of a consumer that only
// registered a handler.
func TestLibraryConfigWritesNoDatabase(t *testing.T) {
	dir := t.TempDir()
	logger, err := NewLogger(&Config{OutputDir: dir, LogLevel: "info"})
	if err != nil {
		t.Fatal(err)
	}
	logger.Shutdown()
	if dbs, _ := filepath.Glob(filepath.Join(dir, "logs", "*.db")); len(dbs) > 0 {
		t.Errorf("a Config built in code wrote %v without asking for a capture", dbs)
	}
}

// Asking for it still works, and the binary still asks by default.
func TestCaptureWhenAskedAndForTheBinary(t *testing.T) {
	t.Setenv("FARO_POD_NAME", "")
	dir := t.TempDir()
	logger, err := NewLogger(&Config{OutputDir: dir, LogLevel: "info", SqlitePath: SqliteAuto})
	if err != nil {
		t.Fatal(err)
	}
	logger.Shutdown()
	if _, err := os.Stat(filepath.Join(dir, "logs", "capture.db")); err != nil {
		t.Errorf("SqlitePath auto wrote no capture.db: %v", err)
	}

	cfg := &Config{}
	applyBinaryDefaults(cfg)
	if cfg.SqlitePath != SqliteAuto {
		t.Errorf("the binary's default sqlite_path = %q, want %q", cfg.SqlitePath, SqliteAuto)
	}
	off := &Config{SqlitePath: "off"}
	applyBinaryDefaults(off)
	if off.SqlitePath != "off" {
		t.Errorf("the binary overrode an explicit off: %q", off.SqlitePath)
	}
}

package faro

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// The binary goes through LoadConfig, and must keep capturing to SQLite by
// default - only a Config built in code changed. LoadConfig parses the
// process's flags, which a test can do once, so each case runs in a child
// copy of this test binary.
func TestLoadConfigKeepsTheBinaryCapturing(t *testing.T) {
	if f := os.Getenv("FARO_LOADCONFIG_FILE"); f != "" {
		os.Args = []string{os.Args[0], "-config", f} // the binary's command line
		cfg, err := LoadConfig()
		if err != nil {
			fmt.Println("ERR", err)
			os.Exit(1)
		}
		fmt.Printf("SQLITE=%q\n", cfg.SqlitePath)
		os.Exit(0)
	}
	dir := t.TempDir()
	for _, tc := range []struct{ yaml, want string }{
		{"output_dir: " + dir + "\n", SqliteAuto},
		{"output_dir: " + dir + "\nsqlite_path: \"off\"\n", "off"},
	} {
		f := filepath.Join(dir, "c.yaml")
		if err := os.WriteFile(f, []byte(tc.yaml), 0o644); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(os.Args[0], "-test.run=^TestLoadConfigKeepsTheBinaryCapturing$")
		cmd.Env = append(os.Environ(), "FARO_LOADCONFIG_FILE="+f)
		out, err := cmd.CombinedOutput()
		if want := fmt.Sprintf("SQLITE=%q", tc.want); err != nil || !strings.Contains(string(out), want) {
			t.Errorf("LoadConfig with %q: %s (err %v), want %s", tc.yaml, strings.TrimSpace(string(out)), err, want)
		}
	}
}

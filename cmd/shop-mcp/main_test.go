package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestResolveDBPathPrecedence(t *testing.T) {
	dir := t.TempDir()
	flagDB := filepath.Join(dir, "flag.db")
	envDB := filepath.Join(dir, "env.db")
	touch(t, flagDB)
	touch(t, envDB)
	t.Chdir(dir)
	touch(t, filepath.Join(dir, dbName))

	t.Setenv(envDBPath, envDB)
	if got, err := resolveDBPath(flagDB); err != nil || got != flagDB {
		t.Fatalf("flag: got %q, %v", got, err)
	}
	if got, err := resolveDBPath(""); err != nil || got != envDB {
		t.Fatalf("env: got %q, %v", got, err)
	}
	t.Setenv(envDBPath, "")
	got, err := resolveDBPath("")
	if err != nil || filepath.Base(got) != dbName || !filepath.IsAbs(got) {
		t.Fatalf("cwd: got %q, %v", got, err)
	}
}

func TestResolveDBPathMissing(t *testing.T) {
	t.Setenv(envDBPath, "")
	_, err := resolveDBPath(filepath.Join(t.TempDir(), "missing.db"))
	if err == nil || !strings.Contains(err.Error(), "database not found") || !strings.Contains(err.Error(), envDBPath) {
		t.Fatalf("err = %v", err)
	}
}

func TestParseFlags(t *testing.T) {
	var stderr bytes.Buffer
	cfg, err := parseFlags([]string{"--db", "x.db", "--query-timeout", "2s", "--log-level", "debug"}, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.dbPath != "x.db" || cfg.queryTimeout != 2*time.Second || cfg.logLevel.String() != "DEBUG" {
		t.Fatalf("cfg = %+v", cfg)
	}
	for _, args := range [][]string{{"--log-level", "loud"}, {"--query-timeout", "0s"}, {"extra"}} {
		if _, err := parseFlags(args, &stderr); err == nil {
			t.Errorf("parseFlags(%v) succeeded", args)
		}
	}
}

func TestRunMissingDatabase(t *testing.T) {
	var stderr bytes.Buffer
	code := run([]string{"--db", filepath.Join(t.TempDir(), "missing.db")}, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "database not found") {
		t.Fatalf("code = %d, stderr = %q", code, stderr.String())
	}
}

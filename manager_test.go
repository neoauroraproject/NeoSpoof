package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestManagerCreateListPersist(t *testing.T) {
	dir := t.TempDir()
	m, err := openManager(dir, "client")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.create("Germany", "203.0.113.1", 1080, "toml-a"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := m.create("Finland", "203.0.113.2", 1081, "toml-b"); err != nil {
		t.Fatalf("create 2: %v", err)
	}
	// Duplicate SOCKS port is rejected (one port per location).
	if _, err := m.create("Dup", "203.0.113.3", 1080, "toml-c"); err == nil {
		t.Fatal("duplicate socks port should be rejected")
	}
	// Invalid port rejected.
	if _, err := m.create("Bad", "203.0.113.4", 70000, "toml-d"); err == nil {
		t.Fatal("out-of-range port should be rejected")
	}
	if got := len(m.list()); got != 2 {
		t.Fatalf("list len = %d", got)
	}

	// Reopening loads persisted tunnels.
	m2, err := openManager(dir, "client")
	if err != nil {
		t.Fatal(err)
	}
	l := m2.list()
	if len(l) != 2 || l[0].Name != "Germany" || l[0].SocksPort != 1080 {
		t.Fatalf("reloaded wrong: %#v", l)
	}
	if l[0].Status != "stopped" {
		t.Fatalf("reloaded status = %s", l[0].Status)
	}
}

func TestManagerStartMissingCore(t *testing.T) {
	dir := t.TempDir()
	m, err := openManager(dir, filepath.Join(dir, "no-such-core-binary"))
	if err != nil {
		t.Fatal(err)
	}
	dto, err := m.create("X", "203.0.113.9", 1090, "toml")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.start(dto.ID); err == nil {
		t.Fatal("starting with a missing core binary must fail")
	}
	if st := m.list()[0].Status; st != "error" {
		t.Fatalf("status after failed start = %s", st)
	}
	// The config file should still have been written.
	if _, err := os.Stat(filepath.Join(dir, "tunnels", "1.toml")); err != nil {
		t.Fatalf("config not written: %v", err)
	}
}

func TestManagerRemove(t *testing.T) {
	dir := t.TempDir()
	m, _ := openManager(dir, "client")
	dto, _ := m.create("Y", "203.0.113.5", 2080, "toml")
	if err := m.remove(dto.ID); err != nil {
		t.Fatal(err)
	}
	if len(m.list()) != 0 {
		t.Fatal("tunnel not removed")
	}
}

//go:build linux

package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestManagerSpawnRealProcess spawns a stub "core" (a shell loop), confirming
// the manager starts it, captures its logs, reports it running, and can stop it.
func TestManagerSpawnRealProcess(t *testing.T) {
	dir := t.TempDir()
	stub := filepath.Join(dir, "stub.sh")
	script := "#!/bin/sh\necho \"core up, config=$2\"\nwhile true; do echo tick; sleep 1; done\n"
	if err := os.WriteFile(stub, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	m, err := openManager(dir, stub)
	if err != nil {
		t.Fatal(err)
	}
	dto, err := m.create("Loc", "203.0.113.1", 1080, "dummy-toml", "dummy-server-toml")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.start(dto.ID); err != nil {
		t.Fatalf("start: %v", err)
	}
	time.Sleep(700 * time.Millisecond)

	l := m.list()
	if l[0].Status != "running" {
		t.Fatalf("status = %s, err=%s", l[0].Status, l[0].Error)
	}
	logs, _ := m.logs(dto.ID)
	if len(logs) == 0 {
		t.Fatal("no logs captured from the core process")
	}
	t.Logf("captured %d log lines, first=%q", len(logs), logs[0])

	if err := m.stop(dto.ID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if st := m.list()[0].Status; st != "stopped" {
		t.Fatalf("status after stop = %s", st)
	}
}

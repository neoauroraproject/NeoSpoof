package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCleanIDRejectsJunk(t *testing.T) {
	junk := []string{
		"", "   ", "0", "00000000", "00000000-0000-0000-0000-000000000000",
		"ffffffff-ffff-ffff-ffff-ffffffffffff", "None", "Default string",
		"To be filled by O.E.M.", "Not Specified", "System Serial Number", "unknown",
	}
	for _, j := range junk {
		if got := cleanID(j); got != "" {
			t.Errorf("cleanID(%q) = %q, want empty", j, got)
		}
	}
	for _, ok := range []string{"abc123", "DE:AD:BE:EF", "4c4c4544-0052"} {
		if cleanID(ok) == "" {
			t.Errorf("cleanID(%q) wrongly rejected", ok)
		}
	}
}

func TestFingerprintFromDedupesAndIsStable(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	mid := write("machine-id", "d9b3...stable-id\n")
	dbus := write("dbus-id", "d9b3...stable-id\n") // identical copy
	uuid := write("uuid", "4C4C4544-0052\n")
	junk := write("serial", "To be filled by O.E.M.\n")
	missing := filepath.Join(dir, "does-not-exist")

	fp := fingerprintFrom([]string{mid, dbus, uuid, junk, missing})
	if len(fp) != 64 {
		t.Fatalf("fingerprint %q is not a 64-char sha256 hex", fp)
	}
	// Dropping the duplicate copy and the junk/missing paths must not change the value.
	if again := fingerprintFrom([]string{mid, uuid}); again != fp {
		t.Errorf("fingerprint not stable across dedupe: %q vs %q", fp, again)
	}
	// A different machine must produce a different fingerprint.
	other := write("other-mid", "totally-different\n")
	if diff := fingerprintFrom([]string{other, uuid}); diff == fp {
		t.Error("different machine-id produced the same fingerprint")
	}
}

func TestFingerprintFromEmptyWhenNoSources(t *testing.T) {
	if fp := fingerprintFrom([]string{"/nope/a", "/nope/b"}); fp != "" {
		t.Errorf("want empty fingerprint with no readable sources, got %q", fp)
	}
}

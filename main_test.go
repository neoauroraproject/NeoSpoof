package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func seedReceiver(dir string) *server {
	p := newProbe()
	p.phase = "done"
	p.role = "receiver"
	p.mode = "list"
	p.expected = []string{"8.8.8.8", "1.1.1.1"}
	p.hits = map[string]*tally{
		"8.8.8.8": {preserved: 2, received: 2, wires: map[string]int{"8.8.8.8": 2}},
	}
	p.senderReal = "203.0.113.5"
	lic := &licenseManager{claims: &tokenClaims{Plan: "pro", ExpiresAt: time.Now().Add(time.Hour).Unix()}}
	return &server{probe: p, dataDir: dir, license: lic, settings: &settingsStore{cur: defaultSettings()}}
}

func TestHandleTunnel(t *testing.T) {
	dir := t.TempDir()
	s := seedReceiver(dir)

	rec := httptest.NewRecorder()
	s.handleTunnel(rec, httptest.NewRequest("GET", "/api/tunnel", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Client string     `json:"client"`
		Server string     `json:"server"`
		Meta   tunnelMeta `json:"meta"`
		Saved  []string   `json:"saved"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Meta.Primary != "8.8.8.8" || len(out.Meta.Pool) != 1 {
		t.Fatalf("only the passed IP should be in the pool: %#v", out.Meta)
	}
	if !strings.Contains(out.Client, "8.8.8.8") || strings.Contains(out.Client, "1.1.1.1") {
		t.Fatal("client config should contain only the passed IP")
	}
	if len(out.Saved) != 2 {
		t.Fatalf("expected two saved files, got %v", out.Saved)
	}
	if _, err := os.Stat(filepath.Join(dir, "tunnel-client.toml")); err != nil {
		t.Fatalf("client config not written: %v", err)
	}
}

func TestHandleTunnelRejectsSender(t *testing.T) {
	s := seedReceiver(t.TempDir())
	s.probe.role = "sender" // a sender has no received IPs to build from
	rec := httptest.NewRecorder()
	s.handleTunnel(rec, httptest.NewRequest("GET", "/api/tunnel", nil))
	if rec.Code != 400 {
		t.Fatalf("expected 400 for sender, got %d", rec.Code)
	}
}

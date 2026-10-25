package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// mockLicenseServer signs tokens with priv and binds to *curIP, enforcing that a
// key stays on the IP it first activated from.
func mockLicenseServer(t *testing.T, priv ed25519.PrivateKey, curIP *string) *httptest.Server {
	var bound string
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Key, Nonce, Version string
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		ip := *curIP
		if bound == "" {
			bound = ip
		}
		if bound != ip {
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "bound to another server"})
			return
		}
		claims := tokenClaims{
			Key:       req.Key,
			BoundIP:   ip,
			Plan:      "pro",
			IssuedAt:  time.Now().Unix(),
			ExpiresAt: time.Now().Add(licenseGrace).Unix(),
			Nonce:     req.Nonce,
		}
		payload, _ := json.Marshal(claims)
		sig := ed25519.Sign(priv, payload)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"payload": base64.StdEncoding.EncodeToString(payload),
			"sig":     base64.StdEncoding.EncodeToString(sig),
			"your_ip": ip,
		})
	}))
}

func newTestManager(t *testing.T, pub ed25519.PublicKey, host string) *licenseManager {
	return &licenseManager{
		path:      filepath.Join(t.TempDir(), "license.json"),
		pub:       pub,
		endpoints: []string{host},
		scheme:    "http",
	}
}

func TestLicenseActivateVerifyPersist(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	ip := "203.0.113.7"
	srv := mockLicenseServer(t, priv, &ip)
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")

	m := newTestManager(t, pub, host)
	if err := m.activate("KEY-ABCDEFGH"); err != nil {
		t.Fatalf("activate: %v", err)
	}
	st := m.status()
	if !st.Active || st.Plan != "pro" || st.BoundIP != ip {
		t.Fatalf("status after activate: %#v", st)
	}

	// Persisted token must verify when reloaded from disk.
	raw, err := os.ReadFile(m.path)
	if err != nil {
		t.Fatal(err)
	}
	var stFile licenseState
	if err := json.Unmarshal(raw, &stFile); err != nil {
		t.Fatal(err)
	}
	m2 := &licenseManager{path: m.path, pub: pub, endpoints: []string{host}, scheme: "http"}
	claims, err := m2.verify(stFile.Payload, stFile.Sig)
	if err != nil || claims.Key != "KEY-ABCDEFGH" {
		t.Fatalf("reload verify: %v claims=%#v", err, claims)
	}
}

func TestLicenseRejectsForgedSignature(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	_, otherPriv, _ := ed25519.GenerateKey(rand.Reader) // wrong key
	m := &licenseManager{pub: pub}
	payload, _ := json.Marshal(tokenClaims{Key: "x", ExpiresAt: time.Now().Add(time.Hour).Unix()})
	sig := ed25519.Sign(otherPriv, payload)
	if _, err := m.verify(base64.StdEncoding.EncodeToString(payload), base64.StdEncoding.EncodeToString(sig)); err == nil {
		t.Fatal("a token signed by the wrong key must be rejected")
	}
}

func TestLicenseRejectsMovedServer(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	ip := "203.0.113.7"
	srv := mockLicenseServer(t, priv, &ip)
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")

	m := newTestManager(t, pub, host)
	if err := m.activate("KEY-ABCDEFGH"); err != nil {
		t.Fatalf("activate: %v", err)
	}
	ip = "198.51.100.9" // binary copied to a different server
	if err := m.recheck(); err == nil {
		t.Fatal("recheck from a different IP must fail")
	}
}

func TestLicenseExpiryAndRollback(t *testing.T) {
	m := &licenseManager{}
	// Expired token, honest clock → inactive.
	m.claims = &tokenClaims{Plan: "pro", ExpiresAt: time.Now().Add(-time.Hour).Unix()}
	if m.status().Active {
		t.Fatal("expired token must be inactive")
	}
	// Valid window, but a future high-water mark (clock was rolled back) → the
	// monotonic mark keeps it expired.
	m.claims = &tokenClaims{Plan: "pro", ExpiresAt: time.Now().Add(time.Hour).Unix()}
	m.st.SeenTime = time.Now().Add(2 * time.Hour).Unix()
	if m.status().Active {
		t.Fatal("rolled-back clock must not revive an expired license")
	}
}

func TestOfflineTrial(t *testing.T) {
	dir := t.TempDir()
	// A fresh install (no license, no network) must be active on the trial.
	m, err := openLicense(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	st := m.status()
	if !st.Active || !st.Trial || st.Activated {
		t.Fatalf("fresh install should be in trial: %#v", st)
	}
	if _, err := os.Stat(filepath.Join(dir, "trial.bin")); err != nil {
		t.Fatalf("trial record not written: %v", err)
	}
	// Reopening keeps the same trial window (does not reset it).
	start := m.trial.Start
	m2, _ := openLicense(dir, "")
	if m2.trial == nil || m2.trial.Start != start {
		t.Fatal("trial window reset on reopen")
	}

	// An expired trial is inactive.
	m2.mu.Lock()
	m2.trial = &trialState{Start: time.Now().Add(-48 * time.Hour).Unix(), Seen: time.Now().Add(-48 * time.Hour).Unix()}
	m2.mu.Unlock()
	if m2.status().Active {
		t.Fatal("expired trial must be inactive")
	}

	// A tampered trial record does not decrypt.
	if err := os.WriteFile(filepath.Join(dir, "trial.bin"), []byte("garbage-not-encrypted"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := openTrial([]byte("garbage-not-encrypted")); err == nil {
		t.Fatal("tampered trial record must fail to open")
	}
}

func TestLicenseEndpointsDecode(t *testing.T) {
	eps := licenseEndpoints()
	if len(eps) != 4 || eps[0] != "license.hmray.pro" || eps[3] != "license.hossein-m466.workers.dev" {
		t.Fatalf("endpoints decoded wrong: %#v", eps)
	}
}

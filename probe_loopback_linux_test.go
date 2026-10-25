//go:build linux

package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// TestRawLoopbackSpoof proves the real spoof path end to end on Linux: a packet
// crafted with a spoofed source (192.0.2.1) is sent through a raw socket to the
// loopback and received by the ICMP listener with its source address preserved.
// That preserved source is exactly what the panel reports as "اسپوف باز است".
func TestRawLoopbackSpoof(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("raw sockets need root; run with sudo")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	got := make(chan net.IP, 16)
	go func() {
		_ = runListen(ctx, func(wire net.IP, raw []byte) {
			if _, _, _, _, ok := decodePayload(raw); ok {
				select {
				case got <- wire:
				default:
				}
			}
		})
	}()
	time.Sleep(300 * time.Millisecond) // let the listener bind

	dst := net.ParseIP("127.0.0.1").To4()
	canary := net.ParseIP("192.0.2.1").To4()
	var token [8]byte
	copy(token[:], []byte("spooftkn"))
	pkt := craft(canary, dst, encodePayload(kindCanary, canary, canary, token), 1)

	// Send a few copies; loopback is lossless but a raw packet can still race
	// the listener's bind on a busy box.
	for i := 0; i < 5; i++ {
		if err := runSend(ctx, dst, [][]byte{pkt}, 20*time.Millisecond, func(int) {}); err != nil {
			t.Fatalf("send: %v", err)
		}
	}

	select {
	case wire := <-got:
		if !wire.Equal(canary) {
			t.Fatalf("source was rewritten to %v (spoof NOT preserved)", wire)
		}
		t.Logf("spoofed source %v preserved end to end — spoof path works", wire)
	case <-time.After(3 * time.Second):
		t.Fatal("no spoofed packet arrived at the listener")
	}
}

// TestLiveProbePipeline drives two full probe engines over loopback — one as
// receiver, one as sender — and asserts the receiver reaches the "passed"
// verdict. This exercises start → run → sendAll → runListen → addHit →
// summarize exactly as the HTTP panel would.
func TestLiveProbePipeline(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("raw sockets need root; run with sudo")
	}
	cfg := settings{ListenSeconds: 30, PacketsPerSec: 200}
	rx := newProbe()
	tx := newProbe()

	if err := rx.start("simple", "receiver", "", nil, cfg); err != nil {
		t.Fatalf("receiver start: %v", err)
	}
	defer rx.stop()
	time.Sleep(400 * time.Millisecond) // let the listener bind

	if err := tx.start("simple", "sender", "127.0.0.1", nil, cfg); err != nil {
		t.Fatalf("sender start: %v", err)
	}
	defer tx.stop()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		st := rx.status()
		if st.Verdict == "passed" {
			t.Logf("receiver verdict=passed packets=%d — full pipeline works", st.PacketCount)
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	st := rx.status()
	t.Fatalf("receiver never reached passed: verdict=%q packets=%d detail=%q", st.Verdict, st.PacketCount, st.Detail)
}

// TestLiveTunnelBuild runs a real list probe over loopback and then builds a
// tunnel config straight from the IPs that actually arrived — the full
// "detect → build tunnel" chain with real packets.
func TestLiveTunnelBuild(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("raw sockets need root; run with sudo")
	}
	cfg := settings{ListenSeconds: 30, PacketsPerSec: 200}
	list := []net.IP{net.ParseIP("8.8.8.8").To4(), net.ParseIP("1.1.1.1").To4()}
	rx := newProbe()
	tx := newProbe()

	if err := rx.start("list", "receiver", "", nil, cfg); err != nil {
		t.Fatalf("receiver start: %v", err)
	}
	defer rx.stop()
	time.Sleep(400 * time.Millisecond)
	if err := tx.start("list", "sender", "127.0.0.1", list, cfg); err != nil {
		t.Fatalf("sender start: %v", err)
	}
	defer tx.stop()

	var passing []string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if p, _, _, _, _ := rx.tunnelData(); len(p) >= 2 {
			passing = p
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(passing) < 2 {
		t.Fatalf("expected both IPs to pass, got %v", passing)
	}

	lic := &licenseManager{claims: &tokenClaims{Plan: "pro", ExpiresAt: time.Now().Add(time.Hour).Unix()}}
	s := &server{probe: rx, dataDir: t.TempDir(), license: lic}
	rec := httptest.NewRecorder()
	s.handleTunnel(rec, httptest.NewRequest("GET", "/api/tunnel", nil))
	if rec.Code != 200 {
		t.Fatalf("tunnel status %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Client string     `json:"client"`
		Meta   tunnelMeta `json:"meta"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Client, "8.8.8.8") || !strings.Contains(out.Client, "1.1.1.1") {
		t.Fatalf("generated client config missing passed IPs:\n%s", out.Client)
	}
	t.Logf("built tunnel from live packets: pool=%v senderReal=%s", out.Meta.Pool, out.Meta.PeerReal)
}

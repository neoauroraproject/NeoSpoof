package main

import (
	"math"
	"strings"
	"testing"
)

func TestPingStats(t *testing.T) {
	min, avg, jitter := pingStats([]float64{30, 50, 40})
	if min != 30 {
		t.Errorf("min = %v, want 30", min)
	}
	if math.Abs(avg-40) > 1e-9 {
		t.Errorf("avg = %v, want 40", avg)
	}
	// |50-30| + |40-50| = 20 + 10 = 30, over 2 gaps → 15
	if math.Abs(jitter-15) > 1e-9 {
		t.Errorf("jitter = %v, want 15", jitter)
	}
}

func TestPingStatsSingleSample(t *testing.T) {
	min, avg, jitter := pingStats([]float64{42})
	if min != 42 || avg != 42 || jitter != 0 {
		t.Errorf("single sample: got min=%v avg=%v jitter=%v, want 42,42,0", min, avg, jitter)
	}
}

func TestApplyAndReadTune(t *testing.T) {
	toml := "real_ip = \"1.2.3.4\"\ntunnel_count    = 4\nmtu             = 1380\ninitial_cwnd    = 30.0\nsocks5_port     = 1080\n"
	sug := tuneSuggestion{MTU: 1360, InitialCwnd: 96, TunnelCount: 6}
	out := applyTune(toml, sug)

	got := readTune(out)
	if got.MTU != 1360 {
		t.Errorf("MTU = %d, want 1360", got.MTU)
	}
	if got.TunnelCount != 6 {
		t.Errorf("TunnelCount = %d, want 6", got.TunnelCount)
	}
	if got.InitialCwnd != 96 {
		t.Errorf("InitialCwnd = %v, want 96", got.InitialCwnd)
	}
	// initial_cwnd must stay a float literal so the Rust toml f64 parser accepts it.
	if !strings.Contains(out, "initial_cwnd = 96.0") {
		t.Errorf("initial_cwnd not written as a float literal:\n%s", out)
	}
	// Unrelated lines must be untouched.
	if !strings.Contains(out, "real_ip = \"1.2.3.4\"") || !strings.Contains(out, "socks5_port     = 1080") {
		t.Errorf("unrelated lines changed:\n%s", out)
	}
}

func TestRecommendTuning(t *testing.T) {
	// No measurement → safe defaults.
	d := recommendTuning(0, 0)
	if d.TunnelCount != 4 || d.MTU != 1380 {
		t.Errorf("defaults: got %+v", d)
	}
	// High BDP path (200 Mbps, 150 ms) needs more than one tunnel's worth of window.
	hi := recommendTuning(200, 150)
	if hi.TunnelCount < 2 || hi.TunnelCount > 8 {
		t.Errorf("tunnel count out of range: %+v", hi)
	}
	if hi.InitialCwnd < 20 || hi.InitialCwnd > 512 {
		t.Errorf("cwnd out of range: %+v", hi)
	}
	if hi.BDPKB <= 0 {
		t.Errorf("BDP should be positive for a real measurement: %+v", hi)
	}
	// A fat path should recommend at least as many tunnels as a thin one.
	lo := recommendTuning(10, 30)
	if hi.TunnelCount < lo.TunnelCount {
		t.Errorf("fat path got fewer tunnels (%d) than thin path (%d)", hi.TunnelCount, lo.TunnelCount)
	}
}

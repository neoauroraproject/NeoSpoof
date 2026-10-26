package main

import (
	"net"
	"testing"
)

func TestCraftRoundTrip(t *testing.T) {
	src := net.ParseIP("192.0.2.10").To4()
	dst := net.ParseIP("198.51.100.8").To4()
	real := net.ParseIP("203.0.113.4").To4()
	var token [8]byte
	copy(token[:], []byte("tokentok"))
	payload := encodePayload(kindCanary, src, real, token)
	frame := craft(src, dst, payload, 7)
	if checksum(frame[:20]) != 0 && checksum(frame[:20]) != 0xffff {
		sum := checksum(frame[:20])
		if sum != 0 {
			t.Fatalf("ip checksum residue %#x", sum)
		}
	}
	wire, raw, ok := parseFrame(frame)
	if !ok {
		t.Fatal("frame rejected")
	}
	if !wire.Equal(src) {
		t.Fatalf("wire %s", wire)
	}
	kind, intended, gotReal, gotToken, good := decodePayload(raw)
	if !good || kind != kindCanary || !intended.Equal(src) || !gotReal.Equal(real) || gotToken != token {
		t.Fatalf("payload mismatch kind=%d intended=%v real=%v", kind, intended, gotReal)
	}
}

func TestParseIPList(t *testing.T) {
	ips, bad := parseIPList("192.0.2.1\n192.0.2.1, 198.51.100.2\nnope\n# skip\n0.0.0.0")
	if len(bad) != 2 {
		t.Fatalf("bad %#v", bad)
	}
	if len(ips) != 2 || ips[0].String() != "192.0.2.1" || ips[1].String() != "198.51.100.2" {
		t.Fatalf("ips %#v", ips)
	}
}

func TestParseIPListRangesCIDR(t *testing.T) {
	// Inclusive range (4), last-octet shorthand (4), CIDR /30 (4) = 12 addresses.
	ips, bad := parseIPList("2.146.32.8-2.146.32.11\n2.180.31.240-243\n203.0.113.0/30")
	if len(bad) != 0 {
		t.Fatalf("unexpected bad: %#v", bad)
	}
	if len(ips) != 12 {
		t.Fatalf("expected 12 addresses, got %d", len(ips))
	}
	if ips[0].String() != "2.146.32.8" || ips[3].String() != "2.146.32.11" {
		t.Fatalf("range expansion wrong: %v", ips[:4])
	}
	if ips[4].String() != "2.180.31.240" || ips[6].String() != "2.180.31.242" {
		t.Fatalf("shorthand expansion wrong: %v", ips[4:7])
	}
	// A reversed range is rejected; a huge CIDR is capped at the list limit.
	if _, bad := parseIPList("10.0.0.5-10.0.0.1"); len(bad) != 1 {
		t.Fatalf("reversed range should be bad: %#v", bad)
	}
	big, _ := parseIPList("1.0.0.0/8")
	if len(big) != maxListIPs {
		t.Fatalf("huge CIDR not capped: %d", len(big))
	}
}

func TestSimpleVerdict(t *testing.T) {
	canary := tally{preserved: 2, received: 2, wires: map[string]int{canaryIP: 2}}
	code, _ := simpleVerdict(map[string]*tally{canaryIP: &canary}, 2, false, "")
	if code != "passed" {
		t.Fatalf("preserved canary => %s", code)
	}

	rewritten := tally{received: 3, wires: map[string]int{"203.0.113.9": 3}}
	code, detail := simpleVerdict(map[string]*tally{canaryIP: &rewritten}, 3, true, "")
	if code != "rewritten" || detail == "" {
		t.Fatalf("rewritten => %s %s", code, detail)
	}

	real := tally{preserved: 4, received: 4, wires: map[string]int{"203.0.113.4": 4}}
	code, _ = simpleVerdict(map[string]*tally{"203.0.113.4": &real}, 4, true, "")
	if code != "blocked" {
		t.Fatalf("only real => %s", code)
	}

	code, _ = simpleVerdict(map[string]*tally{}, 0, true, "گوش دادن تمام شد")
	if code != "none" {
		t.Fatalf("empty => %s", code)
	}
}

func TestListSummaryNeedsExpected(t *testing.T) {
	hits := map[string]*tally{
		"192.0.2.10": {preserved: 2, received: 2, wires: map[string]int{"192.0.2.10": 2}},
	}
	_, verdict, _ := summarize("list", "receiver", "done", "گوش دادن تمام شد", nil, hits, 2)
	if verdict != "report" {
		t.Fatalf("partial observation reported as %s", verdict)
	}
	_, verdict, _ = summarize("list", "receiver", "done", "", []string{"192.0.2.10"}, hits, 2)
	if verdict != "passed" {
		t.Fatalf("full list => %s", verdict)
	}
	rows, verdict, _ := summarize("list", "receiver", "done", "", []string{"192.0.2.10", "192.0.2.11"}, hits, 2)
	if verdict != "report" || len(rows) != 2 || rows[1].Status != "missing" {
		t.Fatalf("missing row %#v %s", rows, verdict)
	}
}

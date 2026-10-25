package main

import (
	"strings"
	"testing"
)

func TestBuildTunnelConfig(t *testing.T) {
	passing := []string{"8.8.8.8", "1.1.1.1", "9.9.9.9"}
	client, server, meta, err := buildTunnelConfig(passing, "203.0.113.5", "198.51.100.9")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Primary != "8.8.8.8" || len(meta.Pool) != 3 {
		t.Fatalf("meta %#v", meta)
	}
	if meta.DataPort < 20000 || meta.DataPort >= 60000 {
		t.Fatalf("port out of range: %d", meta.DataPort)
	}
	if meta.ICMPID < 1 || meta.ICMPID > 0xffff {
		t.Fatalf("icmp id out of range: %d", meta.ICMPID)
	}
	if len(meta.PSK) != 64 {
		t.Fatalf("psk length %d", len(meta.PSK))
	}
	// Client points at the server's real IP; server points back at the client's.
	if !strings.Contains(client, `peer_real_ip    = "198.51.100.9"`) {
		t.Fatal("client peer_real_ip missing")
	}
	if !strings.Contains(server, `peer_real_ip    = "203.0.113.5"`) {
		t.Fatal("server peer_real_ip missing")
	}
	// The pool must carry every passing IP, no template markers left behind.
	for _, ip := range passing {
		if !strings.Contains(client, `"`+ip+`"`) {
			t.Fatalf("client missing %s", ip)
		}
	}
	if strings.Contains(client, "{{") || strings.Contains(server, "{{") {
		t.Fatal("unfilled template marker remains")
	}
	// No passing IPs must be rejected.
	if _, _, _, err := buildTunnelConfig(nil, "", ""); err == nil {
		t.Fatal("expected error for empty passing set")
	}
}

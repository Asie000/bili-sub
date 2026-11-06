package cli

import (
	"testing"
)

// TestServeURLEdgeForms covers the address shapes TestServeURL leaves out: an
// IPv6 literal, a wildcard bind, a big port, and the empty string. Every one of
// them goes through the same switch, so together they pin what both the startup
// message and the auto-opened browser get pointed at.
//
// The wildcard cases are the reason the switch exists: a browser handed
// http://0.0.0.0:8080 or http://[::]:8080 may refuse to open it, while loopback
// is always reachable on a wildcard bind.
func TestServeURLEdgeForms(t *testing.T) {
	tests := []struct {
		name string
		addr string
		want string
	}{
		{"IPv6 literal keeps its brackets", "[::1]:8080", "http://[::1]:8080"},
		{"wildcard IPv4 binds loopback", "0.0.0.0:8080", "http://localhost:8080"},
		{"wildcard IPv6 binds loopback", "[::]:8080", "http://localhost:8080"},
		{"port only gains localhost", ":65535", "http://localhost:65535"},
		{"empty address has no host to add", "", "http://"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := serveURL(tc.addr); got != tc.want {
				t.Fatalf("serveURL(%q) = %q, want %q", tc.addr, got, tc.want)
			}
		})
	}
}

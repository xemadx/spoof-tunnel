package main

import (
	"net"
	"testing"

	"github.com/ParsaKSH/spoof-tunnel/panel/internal/tlsconf"
)

func TestPanelAccessURL(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status tlsconf.Status
		want   string
	}{
		{"plain HTTP", tlsconf.Status{}, "http://192.0.2.10:18443/secret/"},
		{"valid TLS uses certificate domain", tlsconf.Status{Enabled: true, Valid: true, Domain: "panel.example.com"}, "https://panel.example.com:18443/secret/"},
		{"disabled TLS", tlsconf.Status{Valid: true, Domain: "panel.example.com"}, "http://192.0.2.10:18443/secret/"},
		{"invalid TLS falls back", tlsconf.Status{Enabled: true, Domain: "panel.example.com"}, "http://192.0.2.10:18443/secret/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := panelAccessURL("192.0.2.10", 18443, "/secret/", tc.status); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// The panel URL and the installer's DNS check must use the inbound IP.
// An outbound lookup only sees the egress address, which differs behind
// NAT (on one live server the egress addresses were 205.252.135.55 and
// 205.252.135.239 while the inbound IP was 45.142.189.164).
func TestIsPublicIPv4(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
	}{
		{"45.142.189.164", true},
		{"205.252.135.55", true},
		{"8.8.8.8", true},
		{"172.32.0.1", true},
		{"100.128.0.1", true},
		{"10.0.0.5", false},
		{"172.16.0.1", false},
		{"172.31.255.255", false},
		{"192.168.1.1", false},
		{"100.64.0.1", false},
		{"100.127.255.255", false},
		{"127.0.0.1", false},
		{"169.254.1.1", false},
		{"0.0.0.0", false},
		{"::1", false},
		{"not-an-ip", false},
	}
	for _, tc := range cases {
		if got := isPublicIPv4(net.ParseIP(tc.ip)); got != tc.want {
			t.Errorf("isPublicIPv4(%s) = %v, want %v", tc.ip, got, tc.want)
		}
	}
}

func TestFirstGlobalUnicastIPv4(t *testing.T) {
	addrs := []net.Addr{
		&net.IPNet{IP: net.ParseIP("127.0.0.1"), Mask: net.CIDRMask(8, 32)},
		&net.IPNet{IP: net.ParseIP("10.77.0.1"), Mask: net.CIDRMask(30, 32)},
		&net.IPNet{IP: net.ParseIP("45.142.189.164"), Mask: net.CIDRMask(24, 32)},
		&net.IPNet{IP: net.ParseIP("192.168.1.5"), Mask: net.CIDRMask(24, 32)},
	}
	if got := firstGlobalUnicastIPv4(addrs); got != "45.142.189.164" {
		t.Errorf("got %q, want %q", got, "45.142.189.164")
	}
	if got := firstGlobalUnicastIPv4(nil); got != "" {
		t.Errorf("nil addrs: got %q, want %q", got, "")
	}
	privateOnly := []net.Addr{
		&net.IPNet{IP: net.ParseIP("10.0.0.2"), Mask: net.CIDRMask(8, 32)},
	}
	if got := firstGlobalUnicastIPv4(privateOnly); got != "" {
		t.Errorf("private-only addrs: got %q, want %q", got, "")
	}
}

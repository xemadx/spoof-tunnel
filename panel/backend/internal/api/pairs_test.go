package api

import (
	"github.com/ParsaKSH/spoof-tunnel/panel/internal/db"
	"strings"
	"testing"
)

func TestPairDirection(t *testing.T) {
	l, r, err := buildPair(pairRequest{Name: "test", LocalIP: "203.0.113.1", RemoteIP: "203.0.113.2", LocalSpoof: "198.51.100.1", RemoteSpoof: "198.51.100.2", Listen: "127.0.0.1:5000", Forward: "127.0.0.1:51820", Upload: "tcp", Download: "udp", LocalPort: 5001, RemotePort: 8090})
	if err != nil {
		t.Fatal(err)
	}
	if l.SendTransport != r.RecvTransport || l.RecvTransport != r.SendTransport || l.RemotePort != r.ListenPort || l.RecvPort != r.ClientPort || l.SpoofIP != r.PeerSpoofIP || r.SpoofIP != l.PeerSpoofIP {
		t.Fatal("mismatched pair")
	}
}

func TestPairAllowsPersianNamesByCharacterCount(t *testing.T) {
	_, _, err := buildPair(pairRequest{Name: strings.Repeat("ت", 80), LocalIP: "203.0.113.1", RemoteIP: "203.0.113.2", LocalSpoof: "198.51.100.1", RemoteSpoof: "198.51.100.2", Listen: "127.0.0.1:5000", Forward: "127.0.0.1:51820", Upload: "tcp", Download: "udp", LocalPort: 5001, RemotePort: 8090})
	if err != nil {
		t.Fatal(err)
	}
}

func TestNewTunPairEnablesBilateralSmartPools(t *testing.T) {
	a, b, err := buildPair(pairRequest{Name: "smart", Engine: "tun", LocalIP: "192.0.2.1", RemoteIP: "192.0.2.2", LocalSpoof: "198.51.100.1", RemoteSpoof: "198.51.100.2", Upload: "icmp", Download: "udp", LocalPort: 5001, RemotePort: 8090})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []db.TunnelInstance{a, b} {
		if c.PoolMode != "adaptive" || !c.AutoPeerSync || c.PoolPPS != 25 {
			t.Fatal("new pair did not enable complementary smart pools")
		}
	}
}

package l3

import (
	"encoding/base64"
	"fmt"
	"net"
	"regexp"
)

// Version is reported in logs and in `spoof-l3 --version`.
// Set at build time with:
//
//	go build -ldflags "-X github.com/ParsaKSH/spoof-tunnel/internal/l3.Version=vX.Y.Z"
//
// and defaults to "dev" for an unstamped build, so a build that was not
// stamped with a release can never masquerade as one (the old hard-coded
// 0.2.0 kept printing after newer panel releases).
var Version = "dev"

// Config describes one authenticated IPv4 point-to-point TUN link.
type Config struct {
	PoolPolicy    PoolPolicy `json:"pool_policy,omitempty"`
	ControlSocket string     `json:"control_socket,omitempty"`
	PoolStateFile string     `json:"pool_state_file,omitempty"`
	AutoPeerSync  bool       `json:"auto_peer_sync,omitempty"`
	Role          string     `json:"role"`
	LocalIP       string     `json:"local_ip"`
	PeerIP        string     `json:"peer_ip"`
	SpoofSource   string     `json:"spoof_source"`
	// SpoofSources defines transmit candidates governed by PoolPolicy. Empty
	// policy preserves legacy rotation. Without candidates SpoofSource is used.
	SpoofSources []string `json:"spoof_sources,omitempty"`
	PeerSpoof    string   `json:"peer_spoof"`
	PeerSpoofs   []string `json:"peer_spoofs,omitempty"`
	Send         string   `json:"send"`
	Receive      string   `json:"receive"`
	SendPort     int      `json:"send_port"`
	ReceivePort  int      `json:"receive_port"`
	Interface    string   `json:"interface"`
	Address      string   `json:"address"`
	PeerAddress  string   `json:"peer_address"`
	MTU          int      `json:"mtu"`
	PSK          string   `json:"psk"`
}

// Stats represents real-time telemetry from an authenticated TUN tunnel.
type Stats struct {
	Event              string  `json:"event"`
	PoolMode           string  `json:"pool_mode,omitempty"`
	PoolReady          bool    `json:"pool_ready"`
	LastRejectedSource string  `json:"last_rejected_source,omitempty"`
	SourceRejected     uint64  `json:"source_rejected"`
	InvalidPackets     uint64  `json:"invalid_packets"`
	MTURejected        uint64  `json:"mtu_rejected"`
	ClockRejected      uint64  `json:"clock_rejected"`
	ClockSkewSeconds   float64 `json:"clock_skew_seconds"`

	Peer      bool    `json:"peer_connected"`
	TX        uint64  `json:"tx_bytes"`
	RX        uint64  `json:"rx_bytes"`
	TXPackets uint64  `json:"tx_packets"`
	RXPackets uint64  `json:"rx_packets"`
	Drops     uint64  `json:"drops"`
	LastPeer  int64   `json:"last_peer"`
	RTTMs     float64 `json:"rtt_ms,omitempty"`
}

var ifaceName = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]{0,14}$`)

func Protocol(s string) int {
	switch s {
	case "icmp":
		return 1
	case "tcp":
		return 6
	case "udp":
		return 17
	case "ipip":
		return 4
	case "experimental":
		return 253
	}
	return 0
}

func unicast4(value string) (net.IP, bool) {
	ip := net.ParseIP(value)
	if ip == nil || ip.To4() == nil || ip.IsUnspecified() || ip.IsMulticast() || ip.Equal(net.IPv4bcast) {
		return nil, false
	}
	return ip.To4(), true
}

// Sources returns the effective, de-duplicated list of spoof source addresses.
func (c Config) Sources() ([]net.IP, error) {
	raw := c.SpoofSources
	if len(raw) > MaxPoolSources {
		return nil, fmt.Errorf("source pool exceeds %d addresses", MaxPoolSources)
	}
	if len(raw) == 0 {
		if c.SpoofSource == "" {
			return nil, fmt.Errorf("at least one spoof source address is required")
		}
		raw = []string{c.SpoofSource}
	}
	seen := make(map[string]bool, len(raw))
	out := make([]net.IP, 0, len(raw))
	for _, value := range raw {
		ip, ok := unicast4(value)
		if !ok {
			return nil, fmt.Errorf("spoof source %q must be a unicast IPv4 address", value)
		}
		key := ip.String()
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, ip)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("at least one spoof source address is required")
	}
	peers, err := c.PeerSources()
	if err != nil {
		return nil, err
	}
	for _, peer := range peers {
		if seen[peer.String()] {
			return nil, fmt.Errorf("spoof source must differ from all peer spoof addresses")
		}
	}
	return out, nil
}

// PeerSources is the explicit allowlist of spoofed sources on the other end.
// The legacy single-address field remains valid for existing configurations.
func (c Config) PeerSources() ([]net.IP, error) {
	raw := c.PeerSpoofs
	if len(raw) == 0 {
		raw = []string{c.PeerSpoof}
	}
	seen := map[string]bool{}
	var ips []net.IP
	for _, value := range raw {
		ip, ok := unicast4(value)
		if !ok {
			return nil, fmt.Errorf("peer spoof %q must be a unicast IPv4 address", value)
		}
		if !seen[ip.String()] {
			ips = append(ips, ip)
			seen[ip.String()] = true
		}
	}
	return ips, nil
}

func (c Config) Validate() error {
	if err := c.PoolPolicy.Validate(); err != nil {
		return err
	}
	if c.Role != "local" && c.Role != "remote" {
		return fmt.Errorf("role must be local or remote")
	}
	for _, v := range []string{c.LocalIP, c.PeerIP} {
		if _, ok := unicast4(v); !ok {
			return fmt.Errorf("endpoint and peer spoof addresses must be unicast IPv4")
		}
	}
	if net.ParseIP(c.LocalIP).Equal(net.ParseIP(c.PeerIP)) {
		return fmt.Errorf("real endpoints must differ")
	}
	if _, err := c.Sources(); err != nil {
		return err
	}
	if Protocol(c.Send) == 0 || Protocol(c.Receive) == 0 {
		return fmt.Errorf("unsupported carrier")
	}
	if c.SendPort < 1 || c.SendPort > 65535 || c.ReceivePort < 1 || c.ReceivePort > 65535 {
		return fmt.Errorf("carrier ports must be 1–65535")
	}
	if !ifaceName.MatchString(c.Interface) {
		return fmt.Errorf("interface must be 1–15 safe characters starting with a letter")
	}
	ip, network, err := net.ParseCIDR(c.Address)
	if err != nil || ip.To4() == nil {
		return fmt.Errorf("TUN address must be IPv4 CIDR")
	}
	peer := net.ParseIP(c.PeerAddress)
	if peer == nil || peer.To4() == nil || !network.Contains(peer) || peer.Equal(ip) {
		return fmt.Errorf("TUN peer must be a different address in the same subnet")
	}
	networkIP := network.IP.To4()
	broadcast := make(net.IP, 4)
	for i := range broadcast {
		broadcast[i] = networkIP[i] | ^network.Mask[i]
	}
	if ip.Equal(networkIP) || ip.Equal(broadcast) || peer.Equal(networkIP) || peer.Equal(broadcast) {
		return fmt.Errorf("TUN addresses must be usable host addresses")
	}
	if network.Contains(net.ParseIP(c.LocalIP)) || network.Contains(net.ParseIP(c.PeerIP)) {
		return fmt.Errorf("TUN subnet must not contain real server endpoints")
	}
	ones, _ := network.Mask.Size()
	if ones < 16 || ones > 30 {
		return fmt.Errorf("TUN subnet prefix must be /16 through /30")
	}
	if c.MTU < 576 || c.MTU > 1300 {
		return fmt.Errorf("MTU must be 576–1300")
	}
	key, err := base64.StdEncoding.DecodeString(c.PSK)
	if err != nil || len(key) != 32 {
		return fmt.Errorf("PSK must be a base64-encoded 32-byte key")
	}
	return nil
}

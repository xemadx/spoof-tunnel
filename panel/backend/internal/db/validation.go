package db

import (
	"fmt"
	"gorm.io/gorm"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ParsaKSH/spoof-tunnel/internal/l3"
)

// Validate checks the actual IPv4 datagram relay contract before persisting it.
func (c *TunnelInstance) Validate() error {
	c.Name = strings.TrimSpace(c.Name)
	if utf8.RuneCountInString(c.Name) == 0 || utf8.RuneCountInString(c.Name) > 100 {
		return fmt.Errorf("name must contain 1–100 characters")
	}
	if c.Engine == "tun" {
		return c.L3Config().Validate()
	}
	if c.Engine != "" && c.Engine != "udp-relay" {
		return fmt.Errorf("unknown engine")
	}
	if c.Mode != "local" && c.Mode != "remote" {
		return fmt.Errorf("mode must be local or remote")
	}
	for _, t := range []string{c.SendTransport, c.RecvTransport} {
		switch t {
		case "tcp", "udp", "icmp", "icmpv6":
		default:
			return fmt.Errorf("unsupported transport: %s", t)
		}
	}
	ipv4 := func(name, value string, optional bool) error {
		if value == "" && optional {
			return nil
		}
		ip := net.ParseIP(value)
		if ip == nil || ip.To4() == nil || ip.IsUnspecified() || ip.IsMulticast() {
			return fmt.Errorf("%s must be a unicast IPv4 address", name)
		}
		return nil
	}
	if err := ipv4("spoof_ip", c.SpoofIP, strings.TrimSpace(c.SpoofIPList) != ""); err != nil {
		return err
	}
	if err := ipv4("peer_spoof_ip", c.PeerSpoofIP, true); err != nil {
		return err
	}
	for _, line := range strings.Split(c.SpoofIPList, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if err := ipv4("spoof_ip_list", line, false); err != nil {
			return err
		}
	}
	endpoint := func(name, value string) error {
		host, port, err := net.SplitHostPort(value)
		if err != nil {
			return fmt.Errorf("%s must be IPv4:port", name)
		}
		if host != "" {
			if ip := net.ParseIP(host); ip == nil || ip.To4() == nil {
				return fmt.Errorf("%s requires IPv4", name)
			}
		}
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("%s port must be 1–65535", name)
		}
		return nil
	}
	ports := []int{c.SpoofPort}
	if c.Mode == "local" {
		if err := endpoint("listen_addr", c.ListenAddr); err != nil {
			return err
		}
		if err := ipv4("remote_addr", c.RemoteAddr, false); err != nil {
			return err
		}
		ports = append(ports, c.RemotePort, c.RecvPort)
	} else {
		if err := endpoint("forward_addr", c.ForwardAddr); err != nil {
			return err
		}
		if err := ipv4("client_ip", c.ClientIP, false); err != nil {
			return err
		}
		ports = append(ports, c.ListenPort, c.ClientPort)
	}
	for _, port := range ports {
		if port < 1 || port > 65535 {
			return fmt.Errorf("ports must be 1–65535")
		}
	}
	return nil
}

func (c TunnelInstance) L3Config() l3.Config {
	peer, sendPort, recvPort := c.RemoteAddr, c.RemotePort, c.RecvPort
	if c.Mode == "remote" {
		peer, sendPort, recvPort = c.ClientIP, c.ClientPort, c.ListenPort
	}
	var sources []string
	if strings.TrimSpace(c.SpoofIPList) != "" {
		for _, line := range strings.Split(c.SpoofIPList, "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") {
				sources = append(sources, line)
			}
		}
	}
	return l3.Config{
		PoolPolicy:   l3.PoolPolicy{Mode: c.PoolMode, ProbePPS: c.PoolPPS},
		AutoPeerSync: c.AutoPeerSync,
		Role:         c.Mode,
		LocalIP:      c.LocalIP,
		PeerIP:       peer,
		SpoofSource:  c.SpoofIP,
		SpoofSources: sources,
		PeerSpoof:    c.PeerSpoofIP,
		PeerSpoofs:   parseIPLines(c.PeerSpoofIPList),
		Send:         c.SendTransport,
		Receive:      c.RecvTransport,
		SendPort:     sendPort,
		ReceivePort:  recvPort,
		Interface:    c.TunInterface,
		Address:      c.TunAddress,
		PeerAddress:  c.TunPeer,
		MTU:          c.TunMTU,
		PSK:          c.PSK,
	}
}

func parseIPLines(value string) []string {
	var ips []string
	for _, line := range strings.Split(value, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			ips = append(ips, line)
		}
	}
	return ips
}

// SaveTunnel serializes validation against the current database contents. The
// API also serializes mutations with start/stop so a validated stopped tunnel
// cannot be started midway through an edit.
func SaveTunnel(database *gorm.DB, c *TunnelInstance, create bool) error {
	if err := c.Validate(); err != nil {
		return err
	}
	return database.Transaction(func(tx *gorm.DB) error {
		if c.RecvTransport == "tcp" {
			port := c.RecvPort
			if c.Mode == "remote" {
				port = c.ListenPort
			}
			var count int64
			if err := tx.Model(&ForwardRule{}).Where("listen_port = ? AND enabled = ?", port, true).Count(&count).Error; err != nil {
				return err
			}
			if count > 0 {
				return fmt.Errorf("local TCP carrier port conflicts with an enabled forward rule")
			}
		}
		if c.Engine == "tun" {
			prefix, _ := netip.ParsePrefix(c.TunAddress)
			prefix = prefix.Masked()
			var all []TunnelInstance
			if err := tx.Omit("spoof_ip_list", "peer_spoof_ip_list").Where("id <> ? AND engine = ?", c.ID, "tun").Find(&all).Error; err != nil {
				return err
			}
			for _, other := range all {
				if other.TunInterface == c.TunInterface {
					return fmt.Errorf("interface %s is already used by %s", c.TunInterface, other.Name)
				}
				existing, err := netip.ParsePrefix(other.TunAddress)
				if err != nil {
					continue
				}
				existing = existing.Masked()
				if prefix.Overlaps(existing) {
					return fmt.Errorf("TUN subnet overlaps %s (%s)", other.Name, existing)
				}
				candidate, previous := c.L3Config(), other.L3Config()
				for _, value := range []string{candidate.LocalIP, candidate.PeerIP} {
					ip, err := netip.ParseAddr(value)
					if err == nil && existing.Contains(ip) {
						return fmt.Errorf("real endpoint is inside TUN subnet of %s", other.Name)
					}
				}
				for _, value := range []string{previous.LocalIP, previous.PeerIP} {
					ip, err := netip.ParseAddr(value)
					if err == nil && prefix.Contains(ip) {
						return fmt.Errorf("TUN subnet contains real endpoint of %s", other.Name)
					}
				}
			}
		}
		if create {
			if err := tx.Create(c).Error; err != nil {
				return err
			}
			return EnsureDefaultPool(tx, c)
		}
		return tx.Save(c).Error
	})
}

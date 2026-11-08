package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad(t *testing.T) {
	dir := t.TempDir()

	// Non-existent file returns nil, nil
	cfg, err := Load(filepath.Join(dir, "nonexistent.json"))
	if err != nil || cfg != nil {
		t.Fatalf("expected nil, nil for missing file; got %v, %v", cfg, err)
	}

	// Invalid JSON returns error
	badFile := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(badFile, []byte("{invalid json"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err = Load(badFile)
	if err == nil {
		t.Fatal("expected error for invalid json")
	}

	// Valid config
	goodFile := filepath.Join(dir, "good.json")
	jsonContent := `{
		"mode": "local",
		"listen": "127.0.0.1:6000",
		"remote": "198.51.100.1",
		"remote_port": 9000,
		"recv_port": 6001,
		"spoof_ip": "1.1.1.1",
		"spoof_port": 8443,
		"peer_spoof_ip": "8.8.8.8",
		"spoof_ip_file": "ips.txt",
		"send_transport": "udp",
		"recv_transport": "tcp",
		"xdp_interface": "eth0"
	}`
	if err := os.WriteFile(goodFile, []byte(jsonContent), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(goodFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Mode != "local" || cfg.XDPInterface != "eth0" || cfg.RemotePort != 9000 {
		t.Fatalf("unexpected parsed config: %+v", cfg)
	}
}

func TestLoadIPListFile(t *testing.T) {
	dir := t.TempDir()

	// Nonexistent file
	_, err := LoadIPListFile(filepath.Join(dir, "missing.txt"))
	if err == nil {
		t.Fatal("expected error on nonexistent file")
	}

	// Empty file
	emptyFile := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(emptyFile, []byte("# only comments\n\n   \n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err = LoadIPListFile(emptyFile)
	if err == nil {
		t.Fatal("expected error on empty IP list file")
	}

	// Invalid IP
	invalidFile := filepath.Join(dir, "invalid.txt")
	if err := os.WriteFile(invalidFile, []byte("1.2.3.4\nnot_an_ip\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err = LoadIPListFile(invalidFile)
	if err == nil {
		t.Fatal("expected error on invalid IP format")
	}

	// IPv6 not supported
	v6File := filepath.Join(dir, "v6.txt")
	if err := os.WriteFile(v6File, []byte("2001:db8::1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err = LoadIPListFile(v6File)
	if err == nil {
		t.Fatal("expected error for IPv6 address")
	}

	// Valid IPv4 list
	validFile := filepath.Join(dir, "valid.txt")
	if err := os.WriteFile(validFile, []byte("# comment\n 1.1.1.1 \n\n8.8.8.8\n# another comment\n"), 0644); err != nil {
		t.Fatal(err)
	}
	ips, err := LoadIPListFile(validFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ips) != 2 || ips[0].String() != "1.1.1.1" || ips[1].String() != "8.8.8.8" {
		t.Fatalf("unexpected parsed IPs: %v", ips)
	}
}

func TestMergeLocal(t *testing.T) {
	// Case 1: Config file sets values, CLI flags are default/empty -> file values used
	c := &Config{
		Listen:        "10.0.0.1:5000",
		Remote:        "198.51.100.1",
		RemotePort:    8091,
		RecvPort:      5002,
		SpoofIP:       "1.2.3.4",
		SpoofPort:     8443,
		PeerSpoofIP:   "5.6.7.8",
		SpoofIPFile:   "ips.txt",
		SendTransport: "udp",
		RecvTransport: "tcp",
		XDPInterface:  "eth1",
	}

	listen, remote, remotePort, recvPort, spoofIP, spoofPort, peerSpoofIP, spoofIPFile, sendTransport, recvTransport, xdpInterface :=
		c.MergeLocal("", "", 0, 0, "", 0, "", "", "", "", "")

	if listen != "10.0.0.1:5000" || remote != "198.51.100.1" || remotePort != 8091 || recvPort != 5002 {
		t.Fatalf("unexpected local merge output: listen=%s remote=%s port=%d", listen, remote, remotePort)
	}
	if spoofIP != "1.2.3.4" || spoofPort != 8443 || peerSpoofIP != "5.6.7.8" || spoofIPFile != "ips.txt" {
		t.Fatalf("unexpected local merge spoof: ip=%s port=%d file=%s", spoofIP, spoofPort, spoofIPFile)
	}
	if sendTransport != "udp" || recvTransport != "tcp" || xdpInterface != "eth1" {
		t.Fatalf("unexpected transport/xdp: send=%s recv=%s xdp=%s", sendTransport, recvTransport, xdpInterface)
	}

	// Case 2: CLI flags override config file
	_, _, _, _, _, _, _, _, _, _, xdpOverride :=
		c.MergeLocal("", "", 0, 0, "", 0, "", "", "", "", "eth2")
	if xdpOverride != "eth2" {
		t.Fatalf("expected CLI flag override 'eth2', got %s", xdpOverride)
	}

	// Case 3: Empty config and empty CLI flags fall back to hardcoded defaults
	emptyConfig := &Config{}
	dListen, dRemote, dRemotePort, dRecvPort, dSpoofIP, dSpoofPort, _, _, _, _, dXDP :=
		emptyConfig.MergeLocal("", "", 0, 0, "", 0, "", "", "", "", "")
	if dListen != "127.0.0.1:5000" || dRemote != "" || dRemotePort != 8090 || dRecvPort != 5001 || dSpoofPort != 443 || dSpoofIP != "" || dXDP != "" {
		t.Fatalf("unexpected fallback defaults: listen=%s remotePort=%d recvPort=%d spoofPort=%d", dListen, dRemotePort, dRecvPort, dSpoofPort)
	}
}

func TestMergeRemote(t *testing.T) {
	// Case 1: Config file values used
	c := &Config{
		ListenPort:    8092,
		Forward:       "127.0.0.1:8080",
		ClientIP:      "198.51.100.2",
		ClientPort:    5003,
		SpoofIP:       "1.2.3.4",
		SpoofPort:     9090,
		PeerSpoofIP:   "5.6.7.8",
		SpoofIPFile:   "ips.txt",
		SendTransport: "tcp",
		RecvTransport: "udp",
		XDPInterface:  "eth0",
	}

	listenPort, forward, clientIP, clientPort, spoofIP, spoofPort, peerSpoofIP, spoofIPFile, sendTransport, recvTransport, xdpInterface :=
		c.MergeRemote(0, "", "", 0, "", 0, "", "", "", "", "")

	if listenPort != 8092 || forward != "127.0.0.1:8080" || clientIP != "198.51.100.2" || clientPort != 5003 {
		t.Fatalf("unexpected remote merge: port=%d fwd=%s ip=%s", listenPort, forward, clientIP)
	}
	if spoofIP != "1.2.3.4" || spoofPort != 9090 || peerSpoofIP != "5.6.7.8" || spoofIPFile != "ips.txt" {
		t.Fatalf("unexpected remote merge spoof: ip=%s port=%d", spoofIP, spoofPort)
	}
	if sendTransport != "tcp" || recvTransport != "udp" || xdpInterface != "eth0" {
		t.Fatalf("unexpected transport/xdp: send=%s recv=%s xdp=%s", sendTransport, recvTransport, xdpInterface)
	}

	// Case 2: CLI flag overrides
	_, _, _, _, _, _, _, _, _, _, xdpOverride :=
		c.MergeRemote(0, "", "", 0, "", 0, "", "", "", "", "eth3")
	if xdpOverride != "eth3" {
		t.Fatalf("expected CLI flag override 'eth3', got %s", xdpOverride)
	}

	// Case 3: Empty config defaults
	emptyConfig := &Config{}
	dPort, dForward, dClientIP, dClientPort, _, dSpoofPort, _, _, _, _, dXDP :=
		emptyConfig.MergeRemote(0, "", "", 0, "", 0, "", "", "", "", "")
	if dPort != 8090 || dForward != "127.0.0.1:51820" || dClientIP != "" || dClientPort != 5001 || dSpoofPort != 8090 || dXDP != "" {
		t.Fatalf("unexpected fallback defaults: port=%d forward=%s clientPort=%d spoofPort=%d", dPort, dForward, dClientPort, dSpoofPort)
	}
}

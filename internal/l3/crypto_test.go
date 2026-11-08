package l3

import (
	"encoding/base64"
	"testing"
)

func validConfig() Config {
	return Config{
		Role:        "local",
		LocalIP:     "192.0.2.1",
		PeerIP:      "192.0.2.2",
		SpoofSource: "198.51.100.10",
		PeerSpoof:   "198.51.100.20",
		Send:        "icmp",
		Receive:     "icmp",
		SendPort:    8090,
		ReceivePort: 5001,
		Interface:   "sptest0",
		Address:     "10.77.0.1/30",
		PeerAddress: "10.77.0.2",
		MTU:         1280,
		PSK:         base64.StdEncoding.EncodeToString(make([]byte, 32)),
	}
}

func TestAuthenticatedFrames(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	a, _ := NewCodec(key, "local")
	b, _ := NewCodec(key, "remote")
	frame := a.Seal(1, []byte("TCP and UDP can be carried as IP"))
	tampered := append([]byte(nil), frame...)
	tampered[len(tampered)-1] ^= 1
	if _, _, err := b.Open(tampered); err == nil {
		t.Fatal("accepted modified ciphertext")
	}
	kind, data, err := b.Open(frame)
	if err != nil || kind != 1 || string(data) != "TCP and UDP can be carried as IP" {
		t.Fatal("roundtrip", err)
	}
	if _, _, err = b.Open(frame); err == nil {
		t.Fatal("accepted replay")
	}
	if _, _, err = a.Open(frame); err == nil {
		t.Fatal("accepted reflection")
	}
	other := make([]byte, 32)
	other[0] = 1
	wrong, _ := NewCodec(base64.StdEncoding.EncodeToString(other), "remote")
	if _, _, err = wrong.Open(a.Seal(2, []byte("probe"))); err == nil {
		t.Fatal("accepted wrong key")
	}
}

func TestReordering(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	a, _ := NewCodec(key, "local")
	b, _ := NewCodec(key, "remote")
	frames := make([][]byte, 1100)
	for i := range frames {
		frames[i] = a.Seal(1, []byte{byte(i)})
	}
	for _, i := range []int{0, 4, 2, 3, 1, 1099, 1098} {
		if _, _, err := b.Open(frames[i]); err != nil {
			t.Fatalf("reordered %d: %v", i, err)
		}
	}
	if _, _, err := b.Open(frames[5]); err == nil {
		t.Fatal("accepted frame outside replay window")
	}
}

func TestMultiSpoofSources(t *testing.T) {
	cfg := validConfig()
	cfg.SpoofSources = []string{"198.51.100.10", "198.51.100.11", "198.51.100.10"} // includes duplicate
	sources, err := cfg.Sources()
	if err != nil {
		t.Fatalf("sources: %v", err)
	}
	if len(sources) != 2 {
		t.Fatalf("expected 2 de-duplicated sources, got %d", len(sources))
	}

	// Colliding with peer spoof must fail
	cfg.SpoofSources = []string{cfg.PeerSpoof}
	if _, err := cfg.Sources(); err == nil {
		t.Fatal("expected error on collision with peer spoof")
	}
}

func TestConfigValidation(t *testing.T) {
	cfg := validConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	badRole := cfg
	badRole.Role = "client"
	if err := badRole.Validate(); err == nil {
		t.Fatal("accepted invalid role")
	}

	sameEndpoints := cfg
	sameEndpoints.PeerIP = sameEndpoints.LocalIP
	if err := sameEndpoints.Validate(); err == nil {
		t.Fatal("accepted identical endpoints")
	}

	badMTU := cfg
	badMTU.MTU = 1500
	if err := badMTU.Validate(); err == nil {
		t.Fatal("accepted MTU > 1300")
	}
}

func TestReplayFilterRejectsFramesBeforeStartup(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	a, _ := NewCodec(key, "local")
	// Old frame sealed now
	frame := a.Seal(1, []byte("in-flight packet"))

	// Simulate a receiver restarting 15 seconds after the frame was created
	b, _ := NewCodec(key, "remote")
	b.startTime += 15 // simulate future start time relative to frame timestamp

	if _, _, err := b.Open(frame); err == nil {
		t.Fatal("accepted frame from before receiver process start time")
	}
}

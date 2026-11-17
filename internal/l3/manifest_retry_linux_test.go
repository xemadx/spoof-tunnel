//go:build linux

package l3

import (
	"encoding/json"
	"net"
	"testing"
	"time"
)

func TestManifestRequestRetriesLostInitialTransfer(t *testing.T) {
	c := validConfig()
	c.SpoofSources = []string{"198.51.100.10", "198.51.100.11"}
	r := newPoolRuntime(c, &PacketConn{pool: NewSourcePool([]net.IP{net.ParseIP("198.51.100.10")}, PoolPolicy{Mode: "legacy"})}, nil)
	token := "0123456789abcdef0123456789abcdef"
	request, _ := json.Marshal(poolMessage{Type: "request", Token: token})
	r.receive(request, "192.0.2.2")
	if r.tx == nil {
		t.Fatal("transfer not created")
	}
	// Every initial chunk is lost. The receiver has no manifest metadata
	// with which to send a need message, so it repeats its request.
	for {
		if _, ok := r.nextChunk(time.Now()); !ok {
			break
		}
	}
	r.lastServedRequest = time.Now().Add(-4 * time.Second)
	r.receive(request, "192.0.2.2")
	if m, ok := r.nextChunk(time.Now()); !ok || m.Index != 0 {
		t.Fatalf("same-token retry did not restart the lost transfer: %+v, %v", m, ok)
	}
}

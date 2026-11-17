//go:build linux

package l3

import (
	"net"
	"syscall"
	"testing"
	"time"
)

func TestPacketCloseWaitsForActiveSender(t *testing.T) {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	p := &PacketConn{send: fd, recv: &net.IPConn{}}
	// Model an active SendFrom holding the descriptor while Close begins.
	p.sendMu.RLock()
	done := make(chan struct{})
	go func() { p.Close(); close(done) }()
	deadline := time.Now().Add(time.Second)
	for !p.closed.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	_, socketErr := syscall.GetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_TYPE)
	p.sendMu.RUnlock()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Close did not finish after sender released socket")
	}
	if socketErr != nil {
		t.Fatalf("send descriptor was closed during an active send: %v", socketErr)
	}
	if _, err := syscall.GetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_TYPE); err == nil {
		t.Fatal("send descriptor leaked after Close")
	}
	if err := p.SendFrom(nil, nil); err != net.ErrClosed {
		t.Fatalf("send after close: %v", err)
	}
}

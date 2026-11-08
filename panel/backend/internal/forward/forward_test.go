package forward

import (
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// TestTCPProxyPipesBytes proves the forwarder is a transparent byte pipe:
// a client dialing the listen port reaches the target and gets its bytes
// back unchanged. The target here is a plain loopback echo server, which
// stands in for "3x-ui on the foreign TUN IP" without needing a TUN.
func TestTCPProxyPipesBytes(t *testing.T) {
	// Target: echo server.
	targetLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer targetLn.Close()
	go func() {
		for {
			c, err := targetLn.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				buf := make([]byte, 1024)
				for {
					n, err := conn.Read(buf)
					if n > 0 {
						if _, werr := conn.Write(buf[:n]); werr != nil {
							return
						}
					}
					if err != nil {
						return
					}
				}
			}(c)
		}
	}()

	fwd := New("127.0.0.1:0", targetLn.Addr().String())
	if err := fwd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer fwd.Stop()

	// The real listen address is only known after Start (port 0).
	listenAddr := fwd.BoundAddr()

	conn, err := net.DialTimeout("tcp", listenAddr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial forwarder: %v", err)
	}
	defer conn.Close()
	msg := "vless-bytes-pass-through"
	if _, err := fmt.Fprint(conn, msg); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(buf) != msg {
		t.Fatalf("got %q, want %q", buf, msg)
	}
	if !fwd.Stats().Listening {
		t.Fatal("forwarder stopped unexpectedly")
	}
	// A client may read bytes before the writer goroutine records its atomic
	// counter. Join the workers before asserting the final, exact totals.
	conn.Close()
	fwd.Stop()
	if s := fwd.Stats(); s.Listening || s.Connections != 1 || s.BytesUp != int64(len(msg)) || s.BytesDown != int64(len(msg)) {
		t.Fatalf("unexpected final stats: %+v", s)
	}
}

// TestStopWithIdleConnection proves Stop cannot hang on a client that
// connected but never sends anything: Stop must close the idle pipe
// and return promptly, even if the connection was accepted in the
// window just before Stop.
func TestStopWithIdleConnection(t *testing.T) {
	targetLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer targetLn.Close()
	go func() {
		for {
			c, err := targetLn.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				buf := make([]byte, 1024)
				for {
					if _, err := conn.Read(buf); err != nil {
						return
					}
				}
			}(c)
		}
	}()
	fwd := New("127.0.0.1:0", targetLn.Addr().String())
	if err := fwd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	conn, err := net.DialTimeout("tcp", fwd.BoundAddr(), 5*time.Second)
	if err != nil {
		t.Fatalf("dial forwarder: %v", err)
	}
	defer conn.Close()
	time.Sleep(200 * time.Millisecond) // let the pipe establish
	done := make(chan struct{})
	go func() {
		fwd.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return within 5s with an idle connection")
	}
	if s := fwd.Stats(); s.Listening {
		t.Fatalf("expected Listening=false after Stop, got %+v", s)
	}
}

// TestDialFailureClosesClient proves a dead target does not hang the
// client: the pipe closes promptly and records the dial error.
func TestDialFailureClosesClient(t *testing.T) {
	fwd := New("127.0.0.1:0", "127.0.0.1:1") // port 1: nothing listens
	if err := fwd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer fwd.Stop()

	conn, err := net.DialTimeout("tcp", fwd.BoundAddr(), 5*time.Second)
	if err != nil {
		t.Fatalf("dial forwarder: %v", err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(12 * time.Second))
	buf := make([]byte, 16)
	if _, err := conn.Read(buf); err == nil {
		t.Fatalf("expected client connection to close on dial failure")
	}
	if s := fwd.Stats(); s.LastError == "" {
		t.Fatalf("expected LastError to be recorded, got empty")
	}
}

func TestMaxConnectionsLimit(t *testing.T) {
	targetLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer targetLn.Close()
	go func() {
		for {
			c, err := targetLn.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				buf := make([]byte, 1024)
				for {
					if _, err := conn.Read(buf); err != nil {
						return
					}
				}
			}(c)
		}
	}()

	fwd := New("127.0.0.1:0", targetLn.Addr().String())
	fwd.MaxConnections = 1
	if err := fwd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer fwd.Stop()

	c1, err := net.DialTimeout("tcp", fwd.BoundAddr(), 5*time.Second)
	if err != nil {
		t.Fatalf("dial 1: %v", err)
	}
	defer c1.Close()
	time.Sleep(100 * time.Millisecond)

	// Second connection should be immediately closed because MaxConnections=1
	c2, err := net.DialTimeout("tcp", fwd.BoundAddr(), 5*time.Second)
	if err != nil {
		t.Fatalf("dial 2: %v", err)
	}
	defer c2.Close()
	_ = c2.SetReadDeadline(time.Now().Add(2 * time.Second))
	b := make([]byte, 1)
	if _, err := c2.Read(b); err == nil {
		t.Fatalf("expected connection over limit to be rejected")
	}
}

func TestIsReadyRejectsWhenPeerDisconnected(t *testing.T) {
	targetLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer targetLn.Close()

	fwd := New("127.0.0.1:0", targetLn.Addr().String())
	var ready atomic.Bool
	fwd.IsReady = func() bool { return ready.Load() }
	if err := fwd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer fwd.Stop()

	// When IsReady returns false, connection should be closed immediately without dial
	c, err := net.DialTimeout("tcp", fwd.BoundAddr(), 5*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	b := make([]byte, 1)
	if _, err := c.Read(b); err == nil {
		t.Fatalf("expected client to be closed when IsReady is false")
	}

	// When IsReady becomes true, dial should succeed
	ready.Store(true)
	c2, err := net.DialTimeout("tcp", fwd.BoundAddr(), 5*time.Second)
	if err != nil {
		t.Fatalf("dial 2: %v", err)
	}
	defer c2.Close()
	time.Sleep(50 * time.Millisecond)
	if fwd.Stats().LastError != "" {
		t.Fatalf("unexpected error when ready: %s", fwd.Stats().LastError)
	}
}

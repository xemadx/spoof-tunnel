// Package forward implements the reverse-tunnel port forwarder.
//
// This is the missing piece between a TUN-only tunnel and a real client
// entry point. The spoof-l3 TUN engine is a virtual cable between two
// servers: it carries IP packets, but it does NOT listen on a public TCP
// port for end-user clients. A client (phone/laptop) cannot dial a TUN
// address, and it cannot dial a tunnel carrier port either (8090/5001
// are carrier identifiers, not TCP listeners).
//
// The forwarder closes that gap, on the entry side only (typically the
// Iran server):
//
//	client (phone) ──TCP──> Iran public IP:LISTEN_PORT
//	                     ──forwarder──> foreign TUN IP:TARGET_PORT
//	                     ──inside spoof TUN──> 3x-ui / Xray on foreign
//
// It is a plain byte pipe: it never decrypts, parses, or rewrites the
// application protocol. A VLESS handshake passes through untouched, so
// the client config keeps the foreign inbound's UUID/transport/security
// and only its address/port point at the Iran entry.
//
// v1 is TCP only. UDP is deliberately rejected by the API layer: a UDP
// forwarder needs session tracking (source address + timeout + cleanup)
// and must not be faked by a TCP-shaped copy. That is a separate v2.
//
// Safety rules enforced by callers (API), not here:
//   - ListenPort must not collide with any tunnel carrier port on any
//     tunnel on this host, or with another enabled forwarder's
//     ListenPort.
//   - Target is always the tunnel's peer TUN address (TunPeer), never an
//     arbitrary public IP, so a rule cannot be used to proxy elsewhere.
package forward

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Forwarder is one TCP listen -> target pipe.
type Forwarder struct {
	ReadyTimeout  time.Duration // Set before Start; zero means five seconds.
	BindInterface string        // Set before Start; confines target dials to this TUN.
	ListenAddr    string        // e.g. "0.0.0.0:443"
	TargetAddr    string        // e.g. "10.77.0.2:443" (peer TUN IP + inbound port)

	lifecycle sync.Mutex
	cancel    context.CancelFunc
	mu        sync.Mutex
	listener  net.Listener
	wg        sync.WaitGroup
	listening bool
	lastErr   string
	// Active pipes, so Stop can close idle long-lived connections
	// (e.g. a VLESS session) instead of waiting for them forever.
	conns map[net.Conn]struct{}

	connections int64
	bytesUp     int64 // client -> target
	bytesDown   int64 // target -> client
}

type countingWriter struct {
	w       io.Writer
	counter *int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	atomic.AddInt64(c.counter, int64(n))
	return n, err
}

// Stats is a snapshot for the panel UI.
type Stats struct {
	Listening   bool   `json:"listening"`
	Connections int64  `json:"connections"`
	BytesUp     int64  `json:"bytes_up"`
	BytesDown   int64  `json:"bytes_down"`
	LastError   string `json:"last_error"`
}

// New returns a stopped forwarder. It binds nothing until Start.
func New(listenAddr, targetAddr string) *Forwarder {
	return &Forwarder{ListenAddr: listenAddr, TargetAddr: targetAddr}
}

// Start binds the listen port and starts accepting. Starting an already
// running forwarder is a no-op.
func (f *Forwarder) Start() error {
	f.lifecycle.Lock()
	defer f.lifecycle.Unlock()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listening {
		return nil
	}
	ln, err := net.Listen("tcp", f.ListenAddr)
	if err != nil {
		f.lastErr = err.Error()
		return fmt.Errorf("forward listen %s: %w", f.ListenAddr, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	f.listener, f.cancel = ln, cancel
	f.conns = make(map[net.Conn]struct{})
	f.listening, f.lastErr = true, ""
	f.wg.Add(1)
	go f.acceptLoop(ctx, ln)
	log.Printf("[forward] listening %s -> %s", f.ListenAddr, f.TargetAddr)
	return nil
}

// Stop cancels dialing, closes all active sockets, and joins every worker.
// Serializing lifecycle operations prevents a restart from reusing a WaitGroup
// or connection map while the preceding generation is still draining.
func (f *Forwarder) Stop() {
	f.lifecycle.Lock()
	defer f.lifecycle.Unlock()
	f.mu.Lock()
	if !f.listening {
		f.mu.Unlock()
		return
	}
	f.listening = false
	f.cancel()
	ln := f.listener
	f.listener = nil
	conns := make([]net.Conn, 0, len(f.conns))
	for c := range f.conns {
		conns = append(conns, c)
	}
	f.mu.Unlock()
	_ = ln.Close()
	for _, c := range conns {
		_ = c.Close()
	}
	f.wg.Wait()
	log.Printf("[forward] stopped %s -> %s", f.ListenAddr, f.TargetAddr)
}

// ListenAddr returns the actual bound address (useful when the
// configured port was 0 in tests).
func (f *Forwarder) BoundAddr() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listener == nil {
		return f.ListenAddr
	}
	return f.listener.Addr().String()
}

// Stats returns a point-in-time snapshot.
func (f *Forwarder) Stats() Stats {
	f.mu.Lock()
	defer f.mu.Unlock()
	return Stats{
		Listening:   f.listening,
		Connections: atomic.LoadInt64(&f.connections),
		BytesUp:     atomic.LoadInt64(&f.bytesUp),
		BytesDown:   atomic.LoadInt64(&f.bytesDown),
		LastError:   f.lastErr,
	}
}

var copyBufPool = sync.Pool{
	New: func() any {
		b := make([]byte, 64*1024)
		return &b
	},
}

func (f *Forwarder) acceptLoop(ctx context.Context, ln net.Listener) {
	defer f.wg.Done()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			f.mu.Lock()
			f.lastErr = err.Error()
			f.mu.Unlock()
			select {
			case <-ctx.Done():
				return
			case <-time.After(100 * time.Millisecond):
			}
			continue
		}
		if tcp, ok := conn.(*net.TCPConn); ok {
			_ = tcp.SetNoDelay(true)
			_ = tcp.SetKeepAlive(true)
			_ = tcp.SetKeepAlivePeriod(30 * time.Second)
		}
		f.mu.Lock()
		if ctx.Err() != nil {
			f.mu.Unlock()
			_ = conn.Close()
			return
		}
		f.conns[conn] = struct{}{}
		f.wg.Add(1)
		f.mu.Unlock()
		atomic.AddInt64(&f.connections, 1)
		go f.pipe(ctx, conn)
	}
}

func (f *Forwarder) pipe(ctx context.Context, client net.Conn) {
	defer f.wg.Done()
	defer func() { _ = client.Close(); f.mu.Lock(); delete(f.conns, client); f.mu.Unlock() }()
	dialer := net.Dialer{Timeout: 10 * time.Second}
	if f.BindInterface != "" {
		timeout := f.ReadyTimeout
		if timeout <= 0 {
			timeout = 5 * time.Second
		}
		if err := waitTunnelReady(ctx, f.BindInterface, timeout); err != nil {
			if ctx.Err() == nil {
				f.mu.Lock()
				f.lastErr = err.Error()
				f.mu.Unlock()
			}
			return
		}
		dialer.Control = func(network, address string, raw syscall.RawConn) error {
			return bindTunnelSocket(f.BindInterface, raw)
		}
	}
	target, err := dialer.DialContext(ctx, "tcp", f.TargetAddr)
	if err != nil {
		if ctx.Err() == nil {
			f.mu.Lock()
			f.lastErr = fmt.Sprintf("dial %s: %v", f.TargetAddr, err)
			f.mu.Unlock()
		}
		return
	}
	defer func() { _ = target.Close(); f.mu.Lock(); delete(f.conns, target); f.mu.Unlock() }()
	if tcp, ok := target.(*net.TCPConn); ok {
		_ = tcp.SetNoDelay(true)
		_ = tcp.SetKeepAlive(true)
		_ = tcp.SetKeepAlivePeriod(30 * time.Second)
	}
	f.mu.Lock()
	if ctx.Err() != nil {
		f.mu.Unlock()
		return
	}
	f.conns[target] = struct{}{}
	f.lastErr = ""
	f.mu.Unlock()
	copied := make(chan struct{}, 2)
	copyOne := func(dst, src net.Conn, counter *int64) {
		bufPtr := copyBufPool.Get().(*[]byte)
		defer copyBufPool.Put(bufPtr)
		_, copyErr := io.CopyBuffer(&countingWriter{w: dst, counter: counter}, src, *bufPtr)
		if copyErr != nil {
			_ = dst.Close()
			_ = src.Close()
		} else if tcp, ok := dst.(*net.TCPConn); ok {
			// EOF finishes only this direction; the response may still be in flight.
			_ = tcp.CloseWrite()
		}
		copied <- struct{}{}
	}
	go copyOne(target, client, &f.bytesUp)
	go copyOne(client, target, &f.bytesDown)
	<-copied
	<-copied
}

// Wait only for the configured device; never fall back to another route.
func waitTunnelReady(ctx context.Context, name string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		iface, err := net.InterfaceByName(name)
		if err == nil && iface.Flags&net.FlagUp != 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("TUN %s not ready: %w", name, ctx.Err())
		case <-ticker.C:
		}
	}
}

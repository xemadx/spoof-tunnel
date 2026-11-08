//go:build linux

package l3

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

// PacketConn owns exactly one receive protocol and one raw send socket.
type PacketConn struct {
	tag            []byte
	rejectedSource atomic.Value
	lastSourceLog  time.Time
	rejected       atomic.Uint64
	cfg            Config
	recv           *net.IPConn
	send           int
	sources        [][4]byte
	srcIdx         atomic.Uint64
	peerIP         [4]byte
	peerMu         sync.RWMutex
	peers          map[string]bool
	recvBuf        []byte
	closed         atomic.Bool
	closeOnce      sync.Once
	pool           *SourcePool
	controlRX      []byte
	controlTX      []byte
	peerAccepted   atomic.Bool
	peerReady      atomic.Bool
	unknownWindow  time.Time
	unknownCount   int
}

func NewPacketConn(c Config) (*PacketConn, error) {
	return NewTaggedPacketConn(c, nil)
}

// NewTaggedPacketConn isolates an ephemeral benchmark from production frames.
// The tag is a demultiplexer, never an authentication substitute.
func NewTaggedPacketConn(c Config, tag []byte) (*PacketConn, error) {
	recv, err := net.ListenIP(fmt.Sprintf("ip4:%d", Protocol(c.Receive)), &net.IPAddr{IP: net.ParseIP(c.LocalIP)})
	if err != nil {
		return nil, fmt.Errorf("receive raw socket: %w", err)
	}
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_RAW, syscall.IPPROTO_RAW)
	if err != nil {
		recv.Close()
		return nil, err
	}
	if err = syscall.SetsockoptInt(fd, syscall.IPPROTO_IP, syscall.IP_HDRINCL, 1); err != nil {
		recv.Close()
		syscall.Close(fd)
		return nil, err
	}
	if err := syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_SNDTIMEO, &syscall.Timeval{Sec: 1}); err != nil {
		recv.Close()
		syscall.Close(fd)
		return nil, err
	}
	_ = syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, 12 /* SO_PRIORITY */, 6)
	_ = syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_SNDBUF, 4*1024*1024)
	_ = recv.SetReadBuffer(8 * 1024 * 1024)
	if rc, err := recv.SyscallConn(); err == nil {
		_ = rc.Control(func(s uintptr) {
			_ = syscall.SetsockoptInt(int(s), syscall.SOL_SOCKET, 12 /* SO_PRIORITY */, 6)
		})
	}

	rawSources, err := c.Sources()
	if err != nil {
		recv.Close()
		syscall.Close(fd)
		return nil, err
	}
	sources := make([][4]byte, len(rawSources))
	for i, s := range rawSources {
		copy(sources[i][:], s.To4())
	}
	var peer [4]byte
	copy(peer[:], net.ParseIP(c.PeerIP).To4())

	peers, err := c.PeerSources()
	if err != nil {
		recv.Close()
		syscall.Close(fd)
		return nil, err
	}
	allowed := make(map[string]bool, len(peers))
	for _, ip := range peers {
		allowed[ip.String()] = true
	}
	return &PacketConn{
		tag:       append([]byte(nil), tag...),
		pool:      NewSourcePool(rawSources, c.PoolPolicy),
		controlRX: poolAdmissionPrefix(c, true), controlTX: poolAdmissionPrefix(c, false),
		cfg:     c,
		peers:   allowed,
		recvBuf: make([]byte, 65535),
		recv:    recv,
		send:    fd,
		sources: sources,
		peerIP:  peer,
	}, nil
}

func checksum(b []byte) uint16 {
	var n uint32
	for len(b) > 1 {
		n += uint32(binary.BigEndian.Uint16(b))
		b = b[2:]
	}
	if len(b) > 0 {
		n += uint32(b[0]) << 8
	}
	for n>>16 != 0 {
		n = (n & 65535) + (n >> 16)
	}
	return ^uint16(n)
}

func (p *PacketConn) isPeerAllowed(source string) bool {
	p.peerMu.RLock()
	allowed := p.peers[source]
	p.peerMu.RUnlock()
	return allowed
}

func (p *PacketConn) setPeers(peers map[string]bool) {
	p.peerMu.Lock()
	p.peers = peers
	p.peerMu.Unlock()
}

func (p *PacketConn) Send(payload []byte) error     { return p.sendSelected(payload, true) }
func (p *PacketConn) SendData(payload []byte) error { return p.sendSelected(payload, false) }
func (p *PacketConn) sendSelected(payload []byte, control bool) error {
	if !control && p.cfg.PoolPolicy.Normalized().Mode != "legacy" && (!p.peerReady.Load() || (p.cfg.AutoPeerSync && !p.peerAccepted.Load())) {
		return fmt.Errorf("waiting for verified peer readiness and source manifest acknowledgement")
	}
	if p.pool != nil {
		source, err := p.pool.Select(control)
		if err != nil {
			return err
		}
		return p.SendFrom(payload, source)
	}
	return p.SendFrom(payload, nil)
}
func (p *PacketConn) SendFrom(payload []byte, chosen net.IP) error {
	if p.closed.Load() {
		return net.ErrClosed
	}
	if len(p.tag) > 0 {
		payload = append(append([]byte(nil), p.tag...), payload...)
	}
	proto := Protocol(p.cfg.Send)
	extra := 0
	switch proto {
	case 1, 17:
		extra = 8
	case 6:
		extra = 20
	}
	b := make([]byte, 20+extra+len(payload))
	b[0] = 0x45
	b[1] = 0x10 // IPTOS_LOWDELAY (low delay interactive flag for minimal latency and jitter)
	binary.BigEndian.PutUint16(b[2:4], uint16(len(b)))
	b[8] = 64
	b[9] = byte(proto)

	// Rotate source IPv4 round-robin when multiple sources are configured (lockless atomic).
	var src [4]byte
	if len(p.sources) > 0 {
		idx := p.srcIdx.Add(1) - 1
		src = p.sources[idx%uint64(len(p.sources))]
	}
	if chosen != nil {
		copy(src[:], chosen.To4())
	}
	copy(b[12:16], src[:])
	copy(b[16:20], p.peerIP[:])
	binary.BigEndian.PutUint16(b[10:12], checksum(b[:20]))
	h := b[20 : 20+extra]
	switch proto {
	case 1:
		h[0] = 8
		binary.BigEndian.PutUint16(h[4:6], uint16(p.cfg.SendPort))
	case 6:
		binary.BigEndian.PutUint16(h[0:2], uint16(p.cfg.ReceivePort))
		binary.BigEndian.PutUint16(h[2:4], uint16(p.cfg.SendPort))
		h[12] = 5 << 4
		h[13] = 0x18
		binary.BigEndian.PutUint16(h[14:16], 65535)
	case 17:
		binary.BigEndian.PutUint16(h[0:2], uint16(p.cfg.ReceivePort))
		binary.BigEndian.PutUint16(h[2:4], uint16(p.cfg.SendPort))
		binary.BigEndian.PutUint16(h[4:6], uint16(extra+len(payload)))
	}
	copy(b[20+extra:], payload)
	switch proto {
	case 1:
		binary.BigEndian.PutUint16(h[2:4], checksum(b[20:]))
	case 6, 17:
		pseudo := make([]byte, 12+len(b)-20)
		copy(pseudo[:8], b[12:20])
		pseudo[9] = byte(proto)
		binary.BigEndian.PutUint16(pseudo[10:12], uint16(len(b)-20))
		copy(pseudo[12:], b[20:])
		sum := checksum(pseudo)
		if sum == 0 {
			sum = 65535
		}
		off := 6
		if proto == 6 {
			off = 16
		}
		binary.BigEndian.PutUint16(h[off:off+2], sum)
	}
	return syscall.Sendto(p.send, b, 0, &syscall.SockaddrInet4{Addr: p.peerIP})
}

func (p *PacketConn) Receive() ([]byte, error) {
	frame, _, _, err := p.receivePacket(false)
	return frame, err
}
func (p *PacketConn) receivePacket(controlAdmission bool) ([]byte, string, bool, error) {
	b := p.recvBuf
	for {
		n, addr, err := p.recv.ReadFromIP(b)
		if err != nil {
			return nil, "", false, err
		}
		source := addr.IP.String()
		allowed := p.isPeerAllowed(source)

		data := b[:n]
		proto := Protocol(p.cfg.Receive)
		// net.IPConn strips the outer IPv4 header. Parse only the carrier header.
		switch proto {
		case 1:
			if len(data) < 8 || data[0] != 8 || data[1] != 0 || int(binary.BigEndian.Uint16(data[4:6])) != p.cfg.ReceivePort || checksum(data) != 0 {
				continue
			}
			data = data[8:]
		case 6:
			if len(data) < 20 || int(binary.BigEndian.Uint16(data[2:4])) != p.cfg.ReceivePort {
				continue
			}
			h := int(data[12]>>4) * 4
			if h < 20 || h > len(data) {
				continue
			}
			data = data[h:]
		case 17:
			if len(data) < 8 || int(binary.BigEndian.Uint16(data[2:4])) != p.cfg.ReceivePort {
				continue
			}
			length := int(binary.BigEndian.Uint16(data[4:6]))
			if length < 8 || length > len(data) {
				continue
			}
			data = data[8:length]
		}
		if len(p.tag) == 0 && bytes.HasPrefix(data, []byte("SBF1")) {
			continue
		}
		control := len(p.controlRX) > 0 && len(data) > len(p.controlRX) && bytes.Equal(data[:len(p.controlRX)], p.controlRX)
		if len(p.tag) > 0 {
			if !bytes.HasPrefix(data, p.tag) {
				continue
			}
			data = data[len(p.tag):]
		}
		if !allowed {
			if !controlAdmission || !control {
				p.rejected.Add(1)
				p.rejectedSource.Store(source)
				continue
			}
			now := time.Now()
			if now.Sub(p.unknownWindow) >= time.Second {
				p.unknownWindow = now
				p.unknownCount = 0
			}
			if p.unknownCount >= 100 || len(data) > 1500 {
				continue
			}
			p.unknownCount++
		}
		if control {
			data = data[len(p.controlRX):]
		}
		if len(data) > 0 {
			return append([]byte(nil), data...), source, allowed, nil
		}
	}
}

func (p *PacketConn) Close() {
	p.closeOnce.Do(func() {
		p.closed.Store(true)
		_ = p.recv.Close()
		_ = syscall.Close(p.send)
	})
}

func openTun(c Config) (*os.File, error) {
	fd, err := syscall.Open("/dev/net/tun", syscall.O_RDWR|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	var req [40]byte
	copy(req[:16], c.Interface)
	*(*uint16)(unsafe.Pointer(&req[16])) = 0x0001 | 0x1000 | 0x8000 // TUN, NO_PI, exclusive
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), 0x400454ca, uintptr(unsafe.Pointer(&req[0])))
	if errno != 0 {
		syscall.Close(fd)
		return nil, fmt.Errorf("create exclusive TUN %s: %w", c.Interface, errno)
	}
	f := os.NewFile(uintptr(fd), "/dev/net/tun")
	for _, args := range [][]string{
		{"address", "add", c.Address, "dev", c.Interface},
		{"link", "set", "dev", c.Interface, "mtu", fmt.Sprint(c.MTU), "txqueuelen", "1000", "up"},
	} {
		if out, err := exec.Command("ip", args...).CombinedOutput(); err != nil {
			f.Close()
			return nil, fmt.Errorf("configure TUN: %s: %w", out, err)
		}
	}
	return f, nil
}

// Run carries IPv4 packets, plus authenticated challenge/response heartbeats.
func Run(ctx context.Context, c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	codec, err := NewCodec(c.PSK, c.Role)
	if err != nil {
		return err
	}
	conn, err := NewPacketConn(c)
	if err != nil {
		return err
	}
	defer conn.Close()
	suppression := StartBackscatterSuppression(c.Receive, c.ReceivePort)
	defer suppression.Stop()
	tun, err := openTun(c)
	if err != nil {
		return err
	}
	defer tun.Close()
	localTunIP, _, _ := net.ParseCIDR(c.Address)

	var mu sync.Mutex
	stats := Stats{Event: "spoof_stats"}
	pending := make(challenges)
	runtime := newPoolRuntime(c, conn, codec)
	if err := runtime.start(ctx); err != nil {
		return err
	}
	defer runtime.close()

	emit := func() {
		mu.Lock()
		s := stats
		s.SourceRejected = conn.rejected.Load()
		if source := conn.rejectedSource.Load(); source != nil {
			s.LastRejectedSource = source.(string)
		}
		s.PoolMode = c.PoolPolicy.Normalized().Mode
		s.PoolReady = conn.pool.Ready() && (!c.AutoPeerSync || s.PoolMode == "legacy" || conn.peerAccepted.Load())
		if s.PoolMode != "legacy" {
			s.PoolReady = s.PoolReady && conn.peerReady.Load()
		}
		s.Peer = s.LastPeer > time.Now().Add(-15*time.Second).Unix() && s.PoolReady
		if !s.Peer {
			s.RTTMs = 0
			s.JitterMs = 0
		}
		mu.Unlock()
		b, _ := json.Marshal(s)
		log.Print(string(b))
	}

	errCh := make(chan error, 2)
	done := make(chan struct{})
	defer close(done)

	go func() {
		select {
		case <-ctx.Done():
			tun.Close()
			conn.recv.Close()
		case <-done:
		}
	}()

	go func() {
		b := make([]byte, 65535)
		for {
			n, err := tun.Read(b)
			if err != nil {
				errCh <- err
				return
			}

			packets, err := fitIPv4(b[:n], c.MTU)
			if err != nil {
				mu.Lock()
				stats.Drops++
				if errors.Is(err, errMTU) {
					stats.MTURejected++
				} else {
					stats.InvalidPackets++
				}
				mu.Unlock()
				if errors.Is(err, errMTU) {
					if reply := mtuReply(b[:n], c.MTU, localTunIP); reply != nil {
						_, _ = tun.Write(reply)
					}
				}
				continue
			}
			for _, packet := range packets {
				err = conn.SendData(codec.Seal(1, packet))
				mu.Lock()
				if err == nil {
					stats.TX += uint64(len(packet))
					stats.TXPackets++
				} else {
					stats.Drops++
				}
				mu.Unlock()
			}
		}
	}()

	go func() {
		var lastClockLog time.Time
		for {
			frame, observedSource, sourceAllowed, err := conn.receivePacket(true)
			if err != nil {
				errCh <- err
				return
			}
			kind, data, err := codec.Open(frame)
			if err != nil {
				mu.Lock()
				stats.Drops++
				var skew *ClockSkewError
				if errors.As(err, &skew) {
					stats.ClockRejected++
					stats.ClockSkewSeconds = float64(skew.Stamp) - float64(skew.Now)
				}
				mu.Unlock()
				if skew != nil && time.Since(lastClockLog) > 10*time.Second {
					log.Printf("clock diagnostic: %v (offset %.0fs)", err, float64(skew.Stamp)-float64(skew.Now))
					lastClockLog = time.Now()
				}
				continue
			}
			if kind == 5 {
				runtime.receiveProbe(data, observedSource)
				continue
			}
			if kind == 4 {
				runtime.receive(data, observedSource)
				continue
			}
			if !sourceAllowed {
				continue
			}
			runtime.markControlSeen()
			mu.Lock()
			stats.ClockSkewSeconds = 0
			mu.Unlock()
			switch kind {
			case 1:

				packets, packetErr := fitIPv4(data, c.MTU)
				if packetErr != nil {
					mu.Lock()
					stats.Drops++
					if errors.Is(packetErr, errMTU) {
						stats.MTURejected++
					} else {
						stats.InvalidPackets++
					}
					mu.Unlock()
					if errors.Is(packetErr, errMTU) {
						if reply := mtuReply(data, c.MTU, localTunIP); reply != nil {
							_ = conn.SendData(codec.Seal(1, reply))
						}
					}
					continue
				}
				for _, packet := range packets {
					if _, err = tun.Write(packet); err != nil {
						errCh <- err
						return
					}
					mu.Lock()
					stats.RX += uint64(len(packet))
					stats.RXPackets++
					mu.Unlock()
				}
			case 2:
				if len(data) == 16 {
					_ = conn.Send(codec.Seal(3, data))
				}
			case 3:
				mu.Lock()
				if sent, ok := pending.accept(data, time.Now()); ok {
					rtt := float64(time.Since(sent).Microseconds()) / 1000
					if stats.RTTMs > 0 {
						diff := math.Abs(rtt - stats.RTTMs)
						if stats.JitterMs == 0 {
							stats.JitterMs = diff
						} else {
							stats.JitterMs = 0.875*stats.JitterMs + 0.125*diff
						}
					}
					stats.RTTMs = rtt
					runtime.markRoundTrip(time.Since(sent))
					stats.LastPeer = time.Now().Unix()
				}
				mu.Unlock()
			}
		}
	}()

	sources, _ := c.Sources()
	log.Printf("TUN ready %s: interface=%s address=%s peer=%s carrier=%s/%s sources=%d",
		Version, c.Interface, c.Address, c.PeerAddress, c.Send, c.Receive, len(sources))

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-errCh:
			if ctx.Err() != nil || errors.Is(err, os.ErrClosed) {
				return nil
			}
			return err
		case <-ticker.C:
			var challenge [16]byte
			_, err = rand.Read(challenge[:])
			mu.Lock()
			pending.add(challenge, time.Now())
			mu.Unlock()
			payload := challenge[:]
			if err != nil {
				return err
			}
			if err = conn.Send(codec.Seal(2, payload)); err != nil {
				log.Printf("heartbeat send: %v", err)
			}
			emit()
		}
	}
}

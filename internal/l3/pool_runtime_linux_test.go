//go:build linux

package l3

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestSmartPoolRawCarriersAndPeerSync(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires raw sockets")
	}
	for _, send := range []string{"icmp", "udp", "tcp", "ipip", "experimental"} {
		for _, receive := range []string{"icmp", "udp", "tcp", "ipip", "experimental"} {
			t.Run(send+"/"+receive, func(t *testing.T) {
				a := Config{Role: "local", LocalIP: "127.0.0.1", PeerIP: "127.0.0.2", SpoofSources: []string{"127.1.0.1", "127.1.0.2"}, PeerSpoof: "127.3.0.1", Send: send, Receive: receive, SendPort: 19111, ReceivePort: 19112, PSK: base64.StdEncoding.EncodeToString(make([]byte, 32)), MTU: 1280, PoolPolicy: PoolPolicy{Mode: "adaptive", ProbePPS: 100}, AutoPeerSync: true}
				b := a
				b.Role = "remote"
				b.Send, b.Receive = a.Receive, a.Send
				b.LocalIP, b.PeerIP = a.PeerIP, a.LocalIP
				b.SendPort, b.ReceivePort = a.ReceivePort, a.SendPort
				b.SpoofSources = []string{"127.2.0.1", "127.2.0.2"}
				ca, err := NewPacketConn(a)
				if err != nil {
					t.Fatal(err)
				}
				cb, err := NewPacketConn(b)
				if err != nil {
					ca.Close()
					t.Fatal(err)
				}
				coa, _ := NewCodec(a.PSK, a.Role)
				cob, _ := NewCodec(b.PSK, b.Role)
				ra := newPoolRuntime(a, ca, coa)
				rb := newPoolRuntime(b, cb, cob)
				ctx, cancel := context.WithCancel(context.Background())
				var readers sync.WaitGroup
				defer func() {
					cancel()
					ca.Close()
					cb.Close()
					ra.close()
					rb.close()
					readers.Wait()
				}()
				if err = ra.start(ctx); err != nil {
					t.Fatal(err)
				}
				if err = rb.start(ctx); err != nil {
					t.Fatal(err)
				}
				for _, x := range []struct {
					c     *PacketConn
					codec *Codec
					r     *poolRuntime
				}{{ca, coa, ra}, {cb, cob, rb}} {
					readers.Add(1)
					go func(c *PacketConn, codec *Codec, r *poolRuntime) {
						defer readers.Done()
						for {
							frame, source, _, err := c.receivePacket(true)
							if err != nil {
								return
							}
							kind, data, err := codec.Open(frame)
							if err == nil && kind == 5 {
								r.receiveProbe(data, source)
							}
							if err == nil && kind == 4 {
								r.receive(data, source)
							}
						}
					}(x.c, x.codec, x.r)
				}
				deadline := time.Now().Add(10 * time.Second)
				for time.Now().Before(deadline) {
					av, bv := ra.snapshot(0, 50, ""), rb.snapshot(0, 50, "")
					if av.Pool.Healthy == 2 && bv.Pool.Healthy == 2 && av.OurSourcesAccepted && bv.OurSourcesAccepted && av.PeerReady && bv.PeerReady && av.Peer.Count == 2 && bv.Peer.Count == 2 {
						return
					}
					time.Sleep(20 * time.Millisecond)
				}
				t.Fatalf("bilateral discovery/sync failed: A=%+v B=%+v", ra.snapshot(0, 50, ""), rb.snapshot(0, 50, ""))
			})
		}
	}
}

var _ = net.IPv4len

func TestPoolPeerManifestAtomicRepairAndReplay(t *testing.T) {
	cfg := Config{Role: "local", LocalIP: "192.0.2.1", PeerIP: "192.0.2.2", SpoofSource: "203.0.113.1", PeerSpoof: "203.0.113.2", PSK: base64.StdEncoding.EncodeToString(make([]byte, 32)), PoolPolicy: PoolPolicy{Mode: "adaptive"}}
	conn := &PacketConn{pool: NewSourcePool([]net.IP{net.ParseIP(cfg.SpoofSource)}, cfg.PoolPolicy), peers: map[string]bool{cfg.PeerSpoof: true}}
	codec, _ := NewCodec(cfg.PSK, cfg.Role)
	r := newPoolRuntime(cfg, conn, codec)
	r.requestPeer()
	token := r.requestToken
	ips := make([]net.IP, 4096)
	for i := range ips {
		ips[i] = net.IPv4(198, 51, byte(i>>8), byte(i))
	}
	packed, digest, err := packSources(ips)
	if err != nil {
		t.Fatal(err)
	}
	count := (len(packed) + poolChunkBytes - 1) / poolChunkBytes
	if count < 3 {
		t.Fatal("fixture did not span enough chunks")
	}
	chunk := func(i int) poolMessage {
		end := (i + 1) * poolChunkBytes
		if end > len(packed) {
			end = len(packed)
		}
		return poolMessage{Type: "chunk", Token: token, Digest: digest, Index: i, Total: count, Rate: 25, Data: packed[i*poolChunkBytes : end]}
	}
	for i := 0; i < count; i++ {
		if i != 1 {
			r.acceptChunk(chunk(i))
		}
	}
	if len(conn.peers) != 1 || !conn.peers[cfg.PeerSpoof] {
		t.Fatal("partial manifest replaced live allowlist")
	}
	r.maintain(time.Now().Add(3 * time.Second))
	found := false
	for len(r.queue) > 0 {
		m := <-r.queue
		if m.Type == "need" {
			for _, i := range m.Missing {
				if i == 1 {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatal("missing chunk was not requested")
	}
	r.acceptChunk(chunk(1))
	if len(conn.peers) != 4096 || r.view.Revision != digest || r.view.Status != "synced" {
		t.Fatal("repaired manifest failed")
	}
	// Old authenticated chunks can re-acknowledge completion, but cannot create
	// another snapshot or change a later allowlist.
	conn.peers = map[string]bool{"192.0.2.99": true}
	r.acceptChunk(chunk(1))
	if !conn.peers["192.0.2.99"] || len(conn.peers) != 1 {
		t.Fatal("completed transfer replay changed allowlist")
	}
	r.requestPeer()
	wrong := chunk(0)
	r.acceptChunk(wrong)
	if r.rx != nil {
		t.Fatal("old nonce admitted into a new transfer")
	}
	raw, _ := json.Marshal(poolMessage{Type: "ack", Token: "unrelated", Source: cfg.SpoofSource})
	r.receive(raw, cfg.PeerSpoof)
	if conn.pool.Ready() {
		t.Fatal("unsolicited acknowledgement promoted a source")
	}
	if _, err = unpackSources(packed, fmt.Sprint("wrong-", digest)); err == nil {
		t.Fatal("corrupt manifest accepted")
	}
}

func TestExplicitPeerAllowlistEditIsNotOverwrittenByCheckpoint(t *testing.T) {
	cfg := Config{Role: "local", LocalIP: "192.0.2.1", PeerIP: "192.0.2.2", SpoofSource: "203.0.113.1", PeerSpoof: "203.0.113.2", PSK: base64.StdEncoding.EncodeToString(make([]byte, 32)), PoolStateFile: filepath.Join(t.TempDir(), "state.json")}
	pool := NewSourcePool([]net.IP{net.ParseIP(cfg.SpoofSource)}, PoolPolicy{})
	old := newPoolRuntime(cfg, &PacketConn{pool: pool, peers: map[string]bool{}}, nil)
	_, learned, _ := packSources([]net.IP{net.ParseIP("198.51.100.5")})
	disk := poolDisk{Identity: old.identity, ConfiguredPeerRevision: old.configuredPeerRevision, Peer: []string{"198.51.100.5"}, Revision: learned}
	if err := writePoolCheckpoint(cfg.PoolStateFile, disk); err != nil {
		t.Fatal(err)
	}
	cfg.PeerSpoofs = []string{"198.51.100.7"}
	conn := &PacketConn{pool: pool, peers: map[string]bool{"198.51.100.7": true}}
	current := newPoolRuntime(cfg, conn, nil)
	if err := current.start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer current.close()
	if len(conn.peers) != 1 || !conn.peers["198.51.100.7"] {
		t.Fatal("cached manifest overrode explicit administrator configuration")
	}
}

func TestHelloConfirmationIsFreshAndSingleUse(t *testing.T) {
	p, _ := testPool()
	r := &poolRuntime{conn: &PacketConn{pool: p}, digest: "local-digest", helloPending: map[string]time.Time{}, queue: make(chan poolMessage, 4)}
	first := r.hello(time.Now().Add(-6 * time.Second))
	second := r.hello(time.Now())
	if first.Token == second.Token || first.Token == "" {
		t.Fatal("hello nonce reused")
	}
	reply, _ := json.Marshal(poolMessage{Type: "accepted", Token: first.Token, Digest: r.digest})
	r.receive(reply, "192.0.2.2")
	if !r.conn.peerAccepted.Load() || r.lastRoundTrip.IsZero() {
		t.Fatal("valid delayed confirmation rejected")
	}
	before := r.lastRoundTrip
	r.receive(reply, "192.0.2.2")
	if !r.lastRoundTrip.Equal(before) {
		t.Fatal("replayed confirmation refreshed reference")
	}
	r.helloPending[second.Token] = time.Now().Add(-16 * time.Second)
	reply, _ = json.Marshal(poolMessage{Type: "accepted", Token: second.Token, Digest: r.digest})
	r.receive(reply, "192.0.2.2")
	if !r.lastRoundTrip.Equal(before) {
		t.Fatal("expired confirmation refreshed reference")
	}
}

func TestPoolProbesProgressDuringLargeManifest(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires raw sockets")
	}
	sources := make([]string, 15000)
	for i := range sources {
		sources[i] = net.IPv4(127, 64, byte(i>>8), byte(i)).String()
	}
	a := Config{Role: "local", LocalIP: "127.0.0.21", PeerIP: "127.0.0.22", SpoofSources: sources, PeerSpoof: "127.120.0.1", Send: "experimental", Receive: "experimental", SendPort: 29311, ReceivePort: 29312, PSK: base64.StdEncoding.EncodeToString(make([]byte, 32)), MTU: 1280, PoolPolicy: PoolPolicy{Mode: "adaptive", ProbePPS: 25}, AutoPeerSync: true}
	b := a
	b.Role = "remote"
	b.LocalIP, b.PeerIP = a.PeerIP, a.LocalIP
	b.SpoofSources = []string{"127.120.0.1"}
	b.PeerSpoof = sources[0]
	b.SendPort, b.ReceivePort = a.ReceivePort, a.SendPort
	ca, err := NewPacketConn(a)
	if err != nil {
		t.Fatal(err)
	}
	cb, err := NewPacketConn(b)
	if err != nil {
		ca.Close()
		t.Fatal(err)
	}
	coa, _ := NewCodec(a.PSK, a.Role)
	cob, _ := NewCodec(b.PSK, b.Role)
	ra, rb := newPoolRuntime(a, ca, coa), newPoolRuntime(b, cb, cob)
	ctx, cancel := context.WithCancel(context.Background())
	var readers sync.WaitGroup
	defer func() {
		cancel()
		ca.Close()
		cb.Close()
		ra.close()
		rb.close()
		readers.Wait()
	}()
	for _, x := range []struct {
		c     *PacketConn
		codec *Codec
		r     *poolRuntime
	}{{ca, coa, ra}, {cb, cob, rb}} {
		if err := x.r.start(ctx); err != nil {
			t.Fatal(err)
		}
		readers.Add(1)
		go func(c *PacketConn, codec *Codec, r *poolRuntime) {
			defer readers.Done()
			for {
				frame, source, _, err := c.receivePacket(true)
				if err != nil {
					return
				}
				kind, data, err := codec.Open(frame)
				if err != nil {
					continue
				}
				if kind == 4 {
					r.receive(data, source)
				}
				if kind == 5 {
					r.receiveProbe(data, source)
				}
			}
		}(x.c, x.codec, x.r)
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		ra.mu.Lock()
		busy := ra.tx != nil && ra.tx.next < len(ra.tx.chunks)
		ra.mu.Unlock()
		if busy && ca.pool.Ready() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("quality probes were starved by a large outgoing manifest", ra.snapshot(0, 5, ""))
}

func TestSenderRetransmitsMissingChunksWithoutCloggingControlQueue(t *testing.T) {
	conn := &PacketConn{pool: NewSourcePool(nil, PoolPolicy{Mode: "legacy"}), peers: map[string]bool{}}
	cfg := Config{AutoPeerSync: true, PoolPolicy: PoolPolicy{Mode: "adaptive", ProbePPS: 25}}
	r := newPoolRuntime(cfg, conn, nil)

	chunks := make([][]byte, 100)
	for i := range chunks {
		chunks[i] = make([]byte, 160)
	}
	r.tx = &poolTransfer{
		token:  "test-token",
		digest: "test-digest",
		chunks: chunks,
		until:  time.Now().Add(time.Minute),
	}

	missing := make([]uint16, 64)
	for i := range missing {
		missing[i] = uint16(i)
	}
	msg := poolMessage{
		Type:    "need",
		Token:   "test-token",
		Missing: missing,
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	// Deliver 4 consecutive need messages with missing chunks
	for i := 0; i < 4; i++ {
		r.receive(raw, "1.2.3.4")
	}

	// r.queue must NOT contain retransmit chunks!
	if len(r.queue) != 0 {
		t.Fatalf("r.queue should be empty of chunks, got %d items", len(r.queue))
	}

	// Missing chunks must be queued in r.tx.retransmit (deduplicated to 64 items)
	r.mu.Lock()
	retransCount := len(r.tx.retransmit)
	r.mu.Unlock()
	if retransCount != 64 {
		t.Fatalf("expected 64 deduplicated retransmit indices in r.tx.retransmit, got %d", retransCount)
	}

	// nextChunk must serve retransmitted chunks in order
	now := time.Now()
	for i := 0; i < 64; i++ {
		m, ok := r.nextChunk(now)
		if !ok || m.Index != i {
			t.Fatalf("expected retransmit chunk %d, got %v (ok=%v)", i, m.Index, ok)
		}
	}

	// After draining retransmits, nextChunk resumes from r.tx.next (0)
	m, ok := r.nextChunk(now)
	if !ok || m.Index != 0 {
		t.Fatalf("expected fresh chunk 0, got %v (ok=%v)", m.Index, ok)
	}

	// Control queue remains completely unblocked for probe ACKs
	r.enqueue(poolMessage{Type: "ack", Token: "probe-token"})
	if len(r.queue) != 1 {
		t.Fatalf("expected ack in queue, got queue len %d", len(r.queue))
	}
}

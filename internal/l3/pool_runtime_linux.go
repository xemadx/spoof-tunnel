//go:build linux

package l3

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

type poolDisk struct {
	ConfiguredPeerRevision string           `json:"configured_peer_revision,omitempty"`
	Identity               string           `json:"identity"`
	Quarantine             map[string]int64 `json:"quarantine"`
	Peer                   []string         `json:"peer,omitempty"`
	Revision               string           `json:"revision,omitempty"`
	Bootstrap              *PoolBootstrap   `json:"bootstrap,omitempty"`
}

// PoolBootstrap is a one-shot seed written by the panel when a benchmark
// has just proven one source pair with the carriers being applied. The
// engine consumes it once at startup and never writes it back, so a
// later restart re-verifies from scratch. Trust comes from the file
// itself: it lives in the instance's private config directory next to
// the PSK-bearing config, and it is only honored when the source and
// peer source are members of the current configuration and the carriers
// match.
type PoolBootstrap struct {
	Source     string `json:"source"`
	PeerSource string `json:"peer_source"`
	Send       string `json:"send"`
	Receive    string `json:"receive"`
	Expires    int64  `json:"expires"`
}
type poolTransfer struct {
	done          bool
	lastRetry     time.Time
	token, digest string
	chunks        [][]byte
	next          int
	until         time.Time
}
type poolReceive struct {
	lastFresh     time.Time
	token, digest string
	chunks        [][]byte
	received      int
	until         time.Time
}
type poolRuntime struct {
	configuredPeerRevision string
	lastServedRequest      time.Time
	helloPending           map[string]time.Time
	requestStarted         time.Time
	storageError           string
	completedToken         string
	completedDigest        string
	save                   chan struct{}
	cfg                    Config
	conn                   *PacketConn
	codec                  *Codec
	mu                     sync.Mutex
	view                   PeerPoolView
	peerSources            []string
	identity               string
	lastRoundTrip          time.Time
	lastControl            time.Time
	requestToken           string
	requestUntil           time.Time
	lastRequest            time.Time
	tx                     *poolTransfer
	rx                     *poolReceive
	packed                 []byte
	digest                 string
	queue                  chan poolMessage
	listener               net.Listener
	cancel                 context.CancelFunc
	wg                     sync.WaitGroup
}

func newPoolRuntime(c Config, conn *PacketConn, codec *Codec) *poolRuntime {
	// Bind persisted decisions to the authenticated link and carrier tuple.
	raw := fmt.Sprintf("%s|%s|%s|%s|%s|%s|%d|%d", c.PSK, c.Role, c.LocalIP, c.PeerIP, c.Send, c.Receive, c.SendPort, c.ReceivePort)
	sum := sha256.Sum256([]byte(raw))
	r := &poolRuntime{cfg: c, conn: conn, codec: codec, identity: hex.EncodeToString(sum[:]), queue: make(chan poolMessage, 256), save: make(chan struct{}, 1), helloPending: map[string]time.Time{}, view: PeerPoolView{Status: "waiting"}}
	peerSources, _ := c.PeerSources()
	r.view.Status = "configured"
	r.view.Count = len(peerSources)
	_, r.configuredPeerRevision, _ = packSources(peerSources)
	sources, _ := c.Sources()
	r.packed, r.digest, _ = packSources(sources)
	return r
}
func (r *poolRuntime) start(parent context.Context) error {
	ctx, cancel := context.WithCancel(parent)
	r.cancel = cancel
	if r.cfg.PoolStateFile != "" {
		if f, err := os.Open(r.cfg.PoolStateFile); err == nil {
			data, readErr := io.ReadAll(io.LimitReader(f, (32<<20)+1))
			f.Close()
			if readErr != nil || len(data) > 32<<20 {
				data = nil
				log.Print("pool checkpoint unreadable; sources require fresh verification")
			}
			var d poolDisk
			if json.Unmarshal(data, &d) == nil {
				if d.Identity == r.identity {
					r.conn.pool.Restore(d.Quarantine, time.Now())
					if (r.configuredPeerRevision == d.ConfiguredPeerRevision || r.configuredPeerRevision == d.Revision) && r.validatePeer(d.Peer) == nil && len(d.Peer) > 0 {
						r.admitPeer(d.Peer, d.Revision)
					}
				}
				r.consumeBootstrap(d.Bootstrap)
			}
		}
	}
	if path := r.cfg.ControlSocket; path != "" {
		if !filepath.IsAbs(path) {
			cancel()
			return fmt.Errorf("pool socket requires absolute path")
		}
		if old, err := os.Lstat(path); err == nil {
			if old.Mode()&os.ModeSocket == 0 {
				cancel()
				return fmt.Errorf("pool socket path is not a socket")
			}
			_ = os.Remove(path)
		}
		oldUmask := syscall.Umask(0077)
		ln, err := net.Listen("unix", path)
		syscall.Umask(oldUmask)
		if err != nil {
			cancel()
			return err
		}
		r.listener = ln
		_ = os.Chmod(path, 0600)
		r.wg.Add(1)
		go r.serve(ctx)
	}
	if r.cfg.AutoPeerSync {
		r.requestPeer()
	}
	r.wg.Add(1)
	go r.run(ctx)
	return nil
}
func (r *poolRuntime) close() {
	if r.cancel != nil {
		r.cancel()
	}
	if r.listener != nil {
		r.listener.Close()
	}
	r.wg.Wait()
	r.persist()
}
func (r *poolRuntime) markControlSeen() { r.mu.Lock(); r.lastControl = time.Now(); r.mu.Unlock() }
func (r *poolRuntime) markRoundTrip(rtt time.Duration) {
	r.mu.Lock()
	r.lastRoundTrip = time.Now()
	r.mu.Unlock()
	r.conn.pool.ReferenceRTT(rtt)
}
func (r *poolRuntime) hello(now time.Time) poolMessage {
	token := randomPoolToken()
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, sent := range r.helloPending {
		if now.Sub(sent) >= 15*time.Second {
			delete(r.helloPending, id)
		}
	}
	if token == "" || len(r.helloPending) >= 16 {
		return poolMessage{}
	}
	r.helloPending[token] = now
	return poolMessage{Type: "hello", Token: token, Digest: r.digest}
}
func (r *poolRuntime) fresh(now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return now.Sub(r.lastRoundTrip) < 8*time.Second && now.Sub(r.lastControl) < 8*time.Second
}
func (r *poolRuntime) enqueue(m poolMessage) {
	select {
	case r.queue <- m:
	default:
	}
}
func (r *poolRuntime) send(m poolMessage) {
	m.Ready = r.conn.pool.Ready() && (!r.cfg.AutoPeerSync || r.cfg.PoolPolicy.Normalized().Mode == "legacy" || r.conn.peerAccepted.Load())
	b, err := json.Marshal(m)
	if err == nil && len(b) <= 480 {
		_ = r.conn.Send(r.controlFrame(4, b))
	}
}
func randomPoolToken() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}
func (r *poolRuntime) requestPeer() {
	token := randomPoolToken()
	if token == "" {
		return
	}
	r.mu.Lock()
	if r.requestToken != "" && time.Now().Before(r.requestUntil) {
		r.mu.Unlock()
		return
	}
	r.view.InFlight = true
	r.requestStarted = time.Now()
	r.requestToken = token
	r.requestUntil = time.Now().Add(15 * time.Minute)
	r.lastRequest = time.Now()
	r.rx = nil
	r.view.Status = "waiting"
	r.view.Error = ""
	r.view.Progress = 0
	r.mu.Unlock()
	r.enqueue(poolMessage{Type: "request", Token: token})
}
func (r *poolRuntime) run(ctx context.Context) {
	defer r.wg.Done()
	policy := r.cfg.PoolPolicy.Normalized()
	tick := time.NewTicker(time.Second / time.Duration(policy.ProbePPS))
	defer tick.Stop()
	maintenance := time.NewTicker(time.Second)
	defer maintenance.Stop()
	checkpoint := time.NewTicker(30 * time.Second)
	defer checkpoint.Stop()
	nextHello := time.Time{}
	probeTurn := uint64(0)
	nextLog := time.Time{}
	lastLog := time.Time{}
	lastSummary := ""
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-maintenance.C:
			if r.conn.pool.Expire(now, r.fresh(now)) {
				r.persist()
				nextLog = time.Time{}
			}
			if policy.Mode != "legacy" && !now.Before(nextLog) {
				summary := PoolEvent(r.snapshot(0, 1, ""))
				if summary != lastSummary || now.Sub(lastLog) >= time.Minute {
					log.Print(summary)
					lastSummary = summary
					lastLog = now
				}
				nextLog = now.Add(10 * time.Second)
			}
			r.mu.Lock()
			helloEnabled := policy.Mode != "legacy" || r.cfg.AutoPeerSync || r.view.Supported || r.requestToken != "" || len(r.peerSources) > 0
			r.mu.Unlock()
			if helloEnabled && now.After(nextHello) {
				if m := r.hello(now); m.Token != "" {
					r.enqueue(m)
				}
				nextHello = now.Add(5 * time.Second)
			}
			r.maintain(now)
		case <-r.save:
			r.persist()
		case <-checkpoint.C:
			r.persist()
		case now := <-tick.C:
			// Reserve probe service during long manifests and a busy response queue.
			// All classes still share this controller's configured packet budget.
			probeTurn++
			if probeTurn%3 == 0 && r.sendProbe(now) {
				continue
			}
			select {
			case m := <-r.queue:
				r.send(m)
				continue
			default:
			}
			// When chunks are ready to transfer, burst send up to 6 chunks per tick
			// for fast real-time synchronization between both servers.
			chunksSent := 0
			for chunksSent < 6 {
				if m, ok := r.nextChunk(now); ok {
					r.send(m)
					chunksSent++
				} else {
					break
				}
			}
			if chunksSent > 0 {
				continue
			}
			r.sendProbe(now)
		}
	}
}
func (r *poolRuntime) sendProbe(now time.Time) bool {
	token, ip := r.conn.pool.NextProbe(now)
	if token == "" {
		return false
	}
	b := make([]byte, r.conn.pool.ProbeSize(token, r.cfg.MTU))
	raw, _ := hex.DecodeString(token)
	copy(b, raw)
	_ = r.conn.SendFrom(r.controlFrame(5, b), ip)
	return true
}
func (r *poolRuntime) nextChunk(now time.Time) (poolMessage, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := r.tx
	if t == nil || now.After(t.until) || t.next >= len(t.chunks) {
		return poolMessage{}, false
	}
	i := t.next
	t.next++
	return poolMessage{Type: "chunk", Token: t.token, Digest: t.digest, Index: i, Total: len(t.chunks), Rate: r.cfg.PoolPolicy.Normalized().ProbePPS, Data: t.chunks[i]}, true
}
func (r *poolRuntime) maintain(now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.requestToken != "" && !r.view.Supported && now.Sub(r.requestStarted) > 15*time.Second && now.Sub(r.lastRoundTrip) < 8*time.Second {
		r.view.Status = "error"
		r.view.Error = "peer engine does not support smart pools; update both servers"
		r.requestToken = ""
		r.view.InFlight = false
		r.rx = nil
	}
	if r.requestToken != "" {
		if now.After(r.requestUntil) {
			r.view.Status = "error"
			r.view.Error = "peer source synchronization timed out"
			r.requestToken = ""
			r.view.InFlight = false
			r.rx = nil
		} else if now.Sub(r.lastRequest) >= 3*time.Second && r.rx == nil {
			r.enqueue(poolMessage{Type: "request", Token: r.requestToken})
			r.lastRequest = now
		}
	}
	if r.rx != nil && now.Sub(r.rx.lastFresh) >= 500*time.Millisecond {
		r.rx.lastFresh = now
		missing := []uint16{}
		for i, b := range r.rx.chunks {
			if b == nil {
				missing = append(missing, uint16(i))
				if len(missing) == 64 {
					break
				}
			}
		}
		if len(missing) > 0 {
			r.enqueue(poolMessage{Type: "need", Token: r.rx.token, Missing: missing})
		}
	}
	if r.tx != nil && !r.tx.done && r.tx.next >= len(r.tx.chunks) && now.Sub(r.tx.lastRetry) >= time.Second {
		t := r.tx
		i := len(t.chunks) - 1
		if i >= 0 {
			r.enqueue(poolMessage{Type: "chunk", Token: t.token, Digest: t.digest, Index: i, Total: len(t.chunks), Rate: r.cfg.PoolPolicy.Normalized().ProbePPS, Data: t.chunks[i]})
		}
		t.lastRetry = now
	}
	if r.tx != nil && now.After(r.tx.until) {
		r.tx = nil
	}
}
func (r *poolRuntime) receive(data []byte, observed string) {
	if len(data) > 480 {
		return
	}
	var m poolMessage
	if json.Unmarshal(data, &m) != nil {
		return
	}
	r.mu.Lock()
	r.view.Supported = true
	r.lastControl = time.Now()
	r.mu.Unlock()
	switch m.Type {
	case "hello":
		r.mu.Lock()
		matches := m.Digest != "" && m.Digest == r.view.Revision && len(r.peerSources) > 0
		needs := r.cfg.AutoPeerSync && m.Digest != "" && m.Digest != r.view.Revision && r.requestToken == ""
		r.mu.Unlock()
		if matches && len(m.Token) == 32 {
			r.enqueue(poolMessage{Type: "accepted", Token: m.Token, Digest: m.Digest})
		}
		if needs {
			r.requestPeer()
		}
		return
	case "accepted":
		r.mu.Lock()
		sent, ok := r.helloPending[m.Token]
		if ok && m.Digest == r.digest {
			delete(r.helloPending, m.Token)
		} else {
			ok = false
		}
		r.mu.Unlock()
		if ok && time.Since(sent) < 15*time.Second {
			r.conn.peerAccepted.Store(true)
			r.conn.peerReady.Store(m.Ready)
			r.markRoundTrip(time.Since(sent))
		}
	case "probe":
		if len(m.Token) == 32 {
			r.enqueue(poolMessage{Type: "ack", Token: m.Token, Source: observed})
		}
	case "ack":
		if r.conn.pool.Acknowledge(m.Token, m.Source, time.Now()) {
			r.conn.peerReady.Store(m.Ready)
			r.markRoundTrip(0)
		}
	case "request":
		if len(m.Token) != 32 || len(r.packed) == 0 {
			return
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.tx != nil && r.tx.token == m.Token {
			return
		}
		if time.Since(r.lastServedRequest) < 1*time.Second {
			return
		}
		chunks := make([][]byte, 0, (len(r.packed)+poolChunkBytes-1)/poolChunkBytes)
		for i := 0; i < len(r.packed); i += poolChunkBytes {
			end := i + poolChunkBytes
			if end > len(r.packed) {
				end = len(r.packed)
			}
			chunks = append(chunks, r.packed[i:end])
		}
		r.lastServedRequest = time.Now()
		r.tx = &poolTransfer{token: m.Token, digest: r.digest, chunks: chunks, until: time.Now().Add(15*time.Minute + time.Duration(len(chunks)*3/r.cfg.PoolPolicy.Normalized().ProbePPS)*time.Second)}
	case "cancel":
		r.mu.Lock()
		if r.tx != nil && m.Token == r.tx.token {
			r.tx = nil
		}
		r.mu.Unlock()
	case "done":
		r.mu.Lock()
		if r.tx != nil && m.Token == r.tx.token && m.Digest == r.digest {
			r.tx.done = true
			r.conn.peerAccepted.Store(true)
		}
		r.mu.Unlock()
	case "need":
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.tx == nil || r.tx.token != m.Token || len(m.Missing) > 64 {
			return
		}
		for _, i := range m.Missing {
			if int(i) < len(r.tx.chunks) {
				r.enqueue(poolMessage{Type: "chunk", Token: m.Token, Digest: r.tx.digest, Index: int(i), Total: len(r.tx.chunks), Rate: r.cfg.PoolPolicy.Normalized().ProbePPS, Data: r.tx.chunks[i]})
			}
		}
	case "chunk":
		r.acceptChunk(m)
	}
}
func (r *poolRuntime) acceptChunk(m poolMessage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if m.Token != "" && m.Token == r.completedToken && m.Digest == r.completedDigest {
		r.enqueue(poolMessage{Type: "done", Token: m.Token, Digest: m.Digest})
		return
	}
	if m.Token == "" || m.Token != r.requestToken || time.Now().After(r.requestUntil) || m.Total < 1 || m.Total > 8192 || m.Index < 0 || m.Index >= m.Total || len(m.Data) < 1 || len(m.Data) > poolChunkBytes || len(m.Digest) != 64 {
		return
	}
	if r.rx == nil {
		if m.Rate < 1 || m.Rate > 100 {
			return
		}
		until := time.Now().Add(15*time.Minute + time.Duration(m.Total*3/m.Rate)*time.Second)
		if until.After(r.requestUntil) {
			r.requestUntil = until
		}
		r.rx = &poolReceive{token: m.Token, digest: m.Digest, chunks: make([][]byte, m.Total), until: r.requestUntil}
	}
	t := r.rx
	if t.digest != m.Digest || len(t.chunks) != m.Total {
		return
	}
	if t.chunks[m.Index] == nil {
		t.chunks[m.Index] = append([]byte(nil), m.Data...)
		t.received++
		t.lastFresh = time.Now()
	}
	r.view.Status = "receiving"
	r.view.Progress = 100 * t.received / len(t.chunks)
	if t.received != len(t.chunks) {
		return
	}
	packed := make([]byte, 0, t.received*poolChunkBytes)
	for _, b := range t.chunks {
		packed = append(packed, b...)
	}
	ips, err := unpackSources(packed, t.digest)
	values := make([]string, 0, len(ips))
	for _, ip := range ips {
		values = append(values, ip.String())
	}
	if err == nil {
		err = r.validatePeer(values)
	}
	if err != nil {
		r.view.Status = "error"
		r.view.Error = err.Error()
		r.rx = nil
		r.requestToken = ""
		r.view.InFlight = false
		return
	}
	// Called under r.mu: swap the complete allowlist atomically, never per chunk.
	r.admitPeer(values, t.digest)
	r.completedToken = m.Token
	r.completedDigest = t.digest
	r.enqueue(poolMessage{Type: "done", Token: m.Token, Digest: t.digest})
	select {
	case r.save <- struct{}{}:
	default:
	}
	r.requestToken = ""
	r.view.InFlight = false
	r.rx = nil
}
func (r *poolRuntime) validatePeer(values []string) error {
	if len(values) < 1 || len(values) > MaxPoolSources {
		return fmt.Errorf("invalid peer source count")
	}
	for _, v := range values {
		if _, ok := unicast4(v); !ok {
			return fmt.Errorf("invalid peer source")
		}
		if _, own := r.conn.pool.positions[v]; own {
			return fmt.Errorf("peer source pool overlaps this server's source pool")
		}
	}
	return nil
}
func (r *poolRuntime) admitPeer(values []string, revision string) {
	allowed := make(map[string]bool, len(values))
	for _, v := range values {
		allowed[v] = true
	}
	r.conn.setPeers(allowed)
	r.peerSources = append([]string(nil), values...)
	r.view.Count = len(values)
	r.view.Revision = revision
	r.view.Progress = 100
	r.view.Status = "synced"
	r.view.Error = ""
}
func (r *poolRuntime) snapshot(offset, limit int, filter string) PoolRuntimeView {
	view := r.conn.pool.View(offset, limit, filter)
	r.mu.Lock()
	defer r.mu.Unlock()
	return PoolRuntimeView{AutoPeerSync: r.cfg.AutoPeerSync, PeerReady: r.conn.peerReady.Load(), PersistenceError: r.storageError, Pool: view, Peer: r.view, OurSourcesAccepted: r.conn.peerAccepted.Load(), ControlFresh: time.Since(r.lastRoundTrip) < 8*time.Second && time.Since(r.lastControl) < 8*time.Second}
}
func (r *poolRuntime) persist() {
	if r.cfg.PoolStateFile == "" {
		return
	}
	d := poolDisk{ConfiguredPeerRevision: r.configuredPeerRevision, Identity: r.identity, Quarantine: r.conn.pool.Checkpoint()}
	r.mu.Lock()
	d.Peer = append([]string(nil), r.peerSources...)
	d.Revision = r.view.Revision
	r.mu.Unlock()
	err := writePoolCheckpoint(r.cfg.PoolStateFile, d)
	r.mu.Lock()
	previous := r.storageError
	r.storageError = ""
	if err != nil {
		r.storageError = "pool quarantine state could not be saved; check disk space and permissions"
	}
	changed := previous != r.storageError
	r.mu.Unlock()
	if changed && err != nil {
		log.Printf("pool checkpoint: %v", err)
	}
}

// consumeBootstrap applies a benchmark seed written by the panel at
// apply time. It is only honored while fresh, with matching carriers,
// and when both addresses are members of the current configuration.
func (r *poolRuntime) consumeBootstrap(b *PoolBootstrap) {
	if b == nil || time.Now().Unix() >= b.Expires || b.Send != r.cfg.Send || b.Receive != r.cfg.Receive {
		return
	}
	sources, err1 := r.cfg.Sources()
	peers, err2 := r.cfg.PeerSources()
	if err1 != nil || err2 != nil {
		return
	}
	own, peer := false, false
	for _, ip := range sources {
		if ip.String() == b.Source {
			own = true
		}
	}
	for _, ip := range peers {
		if ip.String() == b.PeerSource {
			peer = true
		}
	}
	if !own || !peer {
		return
	}
	ttl := time.Until(time.Unix(b.Expires, 0))
	if ttl < 10*time.Minute {
		ttl = 10 * time.Minute
	}
	if r.conn.pool.SeedVerified(net.ParseIP(b.Source), ttl) {
		log.Printf("pool bootstrap: benchmark-proven source %s pre-verified for %v", b.Source, ttl)
	}
}

func writePoolCheckpoint(path string, value poolDisk) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".pool-state-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	defer f.Close()
	if err = f.Chmod(0600); err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// SeedPoolBootstrap writes a one-shot bootstrap into the pool checkpoint
// file, preserving any existing checkpoint content (quarantine deadlines
// and the learned peer manifest). The engine consumes it once at startup
// and never writes it back.
func SeedPoolBootstrap(file, source, peerSource, send, receive string, ttl time.Duration) error {
	var d poolDisk
	if raw, err := os.ReadFile(file); err == nil {
		_ = json.Unmarshal(raw, &d)
	}
	d.Bootstrap = &PoolBootstrap{Source: source, PeerSource: peerSource, Send: send, Receive: receive, Expires: time.Now().Add(ttl).Unix()}
	return writePoolCheckpoint(file, d)
}

func (r *poolRuntime) serve(ctx context.Context) {
	defer r.wg.Done()
	slots := make(chan struct{}, 8)
	for {
		c, err := r.listener.Accept()
		if err != nil {
			return
		}
		select {
		case slots <- struct{}{}:
			r.wg.Add(1)
			go func() {
				defer r.wg.Done()
				defer func() { <-slots }()
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(5 * time.Second))
				var req PoolControlRequest
				if json.NewDecoder(io.LimitReader(c, 4096)).Decode(&req) != nil {
					return
				}
				response := PoolControlResponse{}
				switch req.Op {
				case "status":
					v := r.snapshot(req.Offset, req.Limit, req.Filter)
					response.Status = &v
				case "sync":
					r.requestPeer()
					v := r.snapshot(0, 1, "")
					response.Status = &v
				case "cancel_sync":
					r.mu.Lock()
					token := r.requestToken
					r.requestToken = ""
					r.rx = nil
					r.view.InFlight = false
					if r.view.Count > 0 {
						r.view.Status = "synced"
					} else {
						r.view.Status = "waiting"
					}
					r.view.Error = ""
					r.mu.Unlock()
					if token != "" {
						r.enqueue(poolMessage{Type: "cancel", Token: token})
					}
				case "peer_sources":
					r.mu.Lock()
					response.Sources = append([]string(nil), r.peerSources...)
					r.mu.Unlock()
				default:
					response.Error = "unknown pool operation"
				}
				_ = json.NewEncoder(c).Encode(response)
			}()
		default:
			c.Close()
		}
		select {
		case <-ctx.Done():
			return
		default:
		}
	}
}

func (r *poolRuntime) receiveProbe(data []byte, observed string) {
	if len(data) < 16 || len(data) > 1300 {
		return
	}
	r.markControlSeen()
	r.mu.Lock()
	r.view.Supported = true
	r.mu.Unlock()
	r.enqueue(poolMessage{Type: "ack", Token: hex.EncodeToString(data[:16]), Source: observed})
}

func (r *poolRuntime) controlFrame(kind byte, payload []byte) []byte {
	frame := r.codec.Seal(kind, payload)
	out := make([]byte, 0, len(r.conn.controlTX)+len(frame))
	out = append(out, r.conn.controlTX...)
	return append(out, frame...)
}

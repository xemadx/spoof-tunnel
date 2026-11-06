package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/ParsaKSH/spoof-tunnel/internal/benchmark"
	"github.com/ParsaKSH/spoof-tunnel/panel/internal/db"
	"github.com/ParsaKSH/spoof-tunnel/panel/internal/manager"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type benchmarkDraft struct {
	Target  uint              `json:"target_id"`
	Name    string            `json:"name"`
	Subnet  string            `json:"subnet"`
	Code    string            `json:"code"`
	Options benchmark.Options `json:"options"`
}
type benchmarkPlan struct {
	Token   string            `json:"token"`
	Index   int               `json:"index"`
	Local   db.TunnelInstance `json:"local"`
	Remote  db.TunnelInstance `json:"remote"`
	Expires int64             `json:"expires"`
}

// A bounded offer, not a serialized database record. A live pool may contain
// hundreds of thousands of entries; none belong in a raw control datagram.
type benchmarkOffer struct {
	Token     string `json:"t"`
	Index     int    `json:"i"`
	Name      string `json:"n"`
	Key       string `json:"k"`
	Interface string `json:"f"`
	Address   string `json:"a"`
	Peer      string `json:"p"`
	MTU       int    `json:"m"`
}

func (p benchmarkPlan) offer() benchmarkOffer {
	return benchmarkOffer{p.Token, p.Index, p.Remote.Name, p.Remote.PSK, p.Remote.TunInterface, p.Remote.TunAddress, p.Remote.TunPeer, p.Remote.TunMTU}
}
func (o benchmarkOffer) candidate(i benchmark.Invitation) db.TunnelInstance {
	return db.TunnelInstance{Name: o.Name, Engine: "tun", Mode: "remote", LocalIP: i.B, ClientIP: i.A, ClientPort: i.PortA, ListenPort: i.PortB, SpoofIP: i.SourceB, PeerSpoofIP: i.SourceA, SpoofPort: 443, TunInterface: o.Interface, TunAddress: o.Address, TunPeer: o.Peer, TunMTU: o.MTU, PSK: o.Key, SendTransport: benchmark.Carriers[o.Index%5], RecvTransport: benchmark.Carriers[o.Index/5], ForwardAddr: "127.0.0.1:51820", PoolMode: "adaptive", PoolPPS: 25, PoolActiveSize: 100, PoolRefreshSeconds: 1800, AutoPeerSync: true}
}

type benchChange struct {
	notify                                 func(error)
	mu                                     sync.Mutex
	s                                      *Server
	before                                 db.TunnelInstance
	after                                  db.TunnelInstance
	existed, running, committed, finalized bool
	token                                  string
	expires                                time.Time
}

var errBenchmarkValidation = errors.New("validation only")

func (s *Server) validateBenchmarkConfig(candidate db.TunnelInstance, create bool) error {
	e := s.db.Transaction(func(tx *gorm.DB) error {
		if e := db.SaveTunnel(tx, &candidate, create); e != nil {
			return e
		}
		return errBenchmarkValidation
	})
	if errors.Is(e, errBenchmarkValidation) {
		return nil
	}
	return e
}
func containsSource(ips []net.IP, value string) bool {
	for _, ip := range ips {
		if ip.String() == value {
			return true
		}
	}
	return false
}

// prioritizeBenchmarkSources moves the benchmark-proven source pair to
// the front of the compiled pool lists. A list that does not contain the
// source is left alone (the single-field fallback still carries it), and
// an empty list stays empty so the pool is never reduced to one source.
func prioritizeBenchmarkSources(inst *db.TunnelInstance, source, peerSource string) {
	inst.SpoofIPList = prioritizeSource(inst.SpoofIPList, source)
	inst.PeerSpoofIPList = prioritizeSource(inst.PeerSpoofIPList, peerSource)
	inst.SpoofIP = source
	inst.PeerSpoofIP = peerSource
}

func prioritizeSource(list, first string) string {
	if strings.TrimSpace(list) == "" || strings.TrimSpace(first) == "" {
		return list
	}
	lines := strings.Split(list, "\n")
	found := false
	out := []string{first}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == first {
			found = true
			continue
		}
		if line != "" {
			out = append(out, line)
		}
	}
	if !found {
		return list
	}
	return strings.Join(out, "\n")
}
func benchmarkEndpoint(c db.TunnelInstance, local, peer, source, peerSource string) error {
	if c.Engine != "tun" {
		return errors.New("connection testing requires the TUN engine")
	}
	cfg := c.L3Config()
	ips, e := cfg.Sources()
	if e != nil {
		return e
	}
	peers, e := cfg.PeerSources()
	if e != nil {
		return e
	}
	if cfg.LocalIP != local {
		return errors.New("benchmark_local_ip_mismatch")
	}
	if cfg.PeerIP != peer {
		return errors.New("benchmark_peer_ip_mismatch")
	}
	if !containsSource(ips, source) {
		return errors.New("benchmark_source_not_in_pool")
	}
	if !containsSource(peers, peerSource) {
		return errors.New("benchmark_peer_source_not_admitted")
	}
	return nil
}
func (s *Server) makeBenchChange(candidate db.TunnelInstance, target uint) (*benchChange, error) {
	change := &benchChange{s: s, after: candidate, expires: time.Now().Add(5 * time.Minute)}
	if target != 0 {
		if e := s.db.First(&change.before, target).Error; e != nil {
			return nil, e
		}
		if !candidate.UpdatedAt.Equal(change.before.UpdatedAt) {
			return nil, errors.New("connection changed after preview; run preview again")
		}
		change.existed = true
		change.after.ID = target
		change.after.CreatedAt = change.before.CreatedAt
		change.after.UpdatedAt = change.before.UpdatedAt
		status, _ := s.manager.InstanceStatus(target)
		change.running = status == manager.StatusRunning
	}
	if e := s.validateBenchmarkConfig(change.after, !change.existed); e != nil {
		return nil, e
	}
	return change, nil
}
func (b *benchChange) unchanged() error {
	if !b.existed && !b.committed {
		return nil
	}
	id := b.before.ID
	if b.committed {
		id = b.after.ID
	}
	var latest db.TunnelInstance
	if e := b.s.db.First(&latest, id).Error; e != nil {
		return e
	}
	expected := b.before.UpdatedAt
	if b.committed {
		expected = b.after.UpdatedAt
	}
	if !latest.UpdatedAt.Equal(expected) {
		return errors.New("connection changed after preview; run preview again")
	}
	return nil
}
func (b *benchChange) commit(source, peerSource string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.committed {
		return nil
	}
	if time.Now().After(b.expires) {
		return errors.New("connection preview expired")
	}
	b.s.mutationMu.Lock()
	defer b.s.mutationMu.Unlock()
	if e := b.unchanged(); e != nil {
		return e
	}
	if e := b.s.validateBenchmarkConfig(b.after, !b.existed); e != nil {
		return e
	}
	if b.existed {
		status, _ := b.s.manager.InstanceStatus(b.before.ID)
		b.running = status == manager.StatusRunning
	}
	if e := b.journal(b.s.db, false); e != nil {
		return e
	}
	if b.existed {
		if e := b.s.manager.StopInstance(b.before.ID); e != nil {
			_ = b.clearJournal(b.s.db)
			return e
		}
	}
	b.after.Enabled = true
	if e := b.s.db.Transaction(func(tx *gorm.DB) error {
		if e := db.SaveTunnel(tx, &b.after, !b.existed); e != nil {
			return e
		}
		return b.journal(tx, true)
	}); e != nil {
		_ = b.clearJournal(b.s.db)
		if b.running {
			_ = b.s.manager.StartInstance(b.before.ID)
		}
		return e
	}
	b.committed = true
	b.expires = time.Now().Add(90 * time.Second)
	// Seed the engine with the benchmark-proven source pair so the new
	// tunnel does not have to rediscover from scratch: without the seed
	// the pool must re-verify sources and re-sync the manifest inside
	// the apply wait, which is what failed a proven pair. Best effort —
	// a seed failure only falls back to normal probing.
	if b.after.Engine == "tun" && source != "" && peerSource != "" {
		if err := b.s.manager.SeedPoolBootstrap(b.after.ID, source, peerSource, b.after.SendTransport, b.after.RecvTransport, 5*time.Minute); err != nil {
			log.Printf("benchmark seed failed for instance %d: %v", b.after.ID, err)
		}
	}
	if e := b.s.manager.StartInstance(b.after.ID); e != nil {
		restore := b.rollbackLocked()
		return errors.Join(e, restore)
	}
	return nil
}

// renew extends the apply lease while the controller is still waiting
// for the peer, so a slow-but-healthy convergence is not rolled back by
// the watch goroutine at 90 seconds.
func (b *benchChange) renew() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.committed && !b.finalized {
		b.expires = time.Now().Add(90 * time.Second)
	}
}
func (b *benchChange) rollbackLocked() error {
	if !b.committed {
		return nil
	}
	if e := b.unchanged(); e != nil {
		return fmt.Errorf("rollback refused to overwrite a newer edit: %w", e)
	}
	if e := b.s.manager.StopInstance(b.after.ID); e != nil {
		return e
	}
	if b.existed {
		restore := b.before
		if e := b.s.db.Transaction(func(tx *gorm.DB) error {
			if e := db.SaveTunnel(tx, &restore, false); e != nil {
				return e
			}
			return b.clearJournal(tx)
		}); e != nil {
			return e
		}
		b.before = restore
		if b.running {
			if e := b.s.manager.StartInstance(b.before.ID); e != nil {
				return e
			}
		}
	} else {
		if e := b.s.db.Transaction(func(tx *gorm.DB) error {
			if e := tx.Where("instance_id = ?", b.after.ID).Delete(&db.ForwardRule{}).Error; e != nil {
				return e
			}
			if e := tx.Where("instance_id = ?", b.after.ID).Delete(&db.PoolFile{}).Error; e != nil {
				return e
			}
			if e := tx.Delete(&db.TunnelInstance{}, b.after.ID).Error; e != nil {
				return e
			}
			return b.clearJournal(tx)
		}); e != nil {
			return e
		}
	}
	b.committed = false
	b.finalized = false
	return nil
}
func (b *benchChange) rollback() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.s.mutationMu.Lock()
	defer b.s.mutationMu.Unlock()
	wasCommitted := b.committed
	e := b.rollbackLocked()
	if wasCommitted && b.notify != nil {
		if e == nil {
			b.notify(errors.New("تغییر اتصال لغو و تنظیمات قبلی بازیابی شد."))
		} else {
			b.notify(e)
		}
	}
	return e
}
func (b *benchChange) watch(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			b.mu.Lock()
			restore := b.committed && !b.finalized
			b.mu.Unlock()
			if restore {
				_ = b.rollback()
			}
			return
		case <-ticker.C:
			b.mu.Lock()
			expired := time.Now().After(b.expires)
			finalized := b.finalized
			b.mu.Unlock()
			if finalized {
				return
			}
			if expired {
				_ = b.rollback()
				return
			}
		}
	}
}
func (s *Server) receiverBenchmarkHook(ctx context.Context, target uint, version time.Time, inv benchmark.Invitation, allowed bool) benchmark.Hook {
	var mu sync.Mutex
	var change *benchChange
	return func(callCtx context.Context, op string, raw json.RawMessage) (any, error) {
		mu.Lock()
		defer mu.Unlock()
		if !allowed {
			return nil, errors.New("enable connection creation on the receiving server before generating its code")
		}
		if op == "apply_prepare" {
			var plan benchmarkOffer
			if json.Unmarshal(raw, &plan) != nil || plan.Token == "" || len(plan.Token) > 64 || plan.Index < 0 || plan.Index >= 25 {
				return nil, errors.New("invalid connection plan")
			}
			if change != nil && change.token == plan.Token {
				return true, nil
			}
			if change != nil {
				change.mu.Lock()
				committed := change.committed
				change.mu.Unlock()
				if committed {
					return nil, errors.New("another plan is already committed")
				}
			}
			candidate := plan.candidate(inv)
			if target != 0 {
				var existing db.TunnelInstance
				if e := s.db.First(&existing, target).Error; e != nil {
					return nil, e
				}
				if !existing.UpdatedAt.Equal(version) {
					return nil, errors.New("receiving connection changed; generate a fresh code")
				}
				if existing.Mode != "remote" || existing.PSK != candidate.PSK || existing.TunAddress != candidate.TunAddress || existing.TunPeer != candidate.TunPeer {
					return nil, errors.New("selected connections are not a complementary pair")
				}
				existing.SendTransport = candidate.SendTransport
				existing.RecvTransport = candidate.RecvTransport
				candidate = existing
			} else {
				candidate.ID = 0
				candidate.CreatedAt = time.Time{}
				candidate.UpdatedAt = time.Time{}
			}
			if candidate.Mode != "remote" || candidate.SendTransport != benchmark.Carriers[plan.Index%5] || candidate.RecvTransport != benchmark.Carriers[plan.Index/5] {
				return nil, errors.New("plan carrier pair does not match the tested result")
			}
			if e := s.benchmarkEndpoint(candidate, inv.B, inv.A, inv.SourceB, inv.SourceA); e != nil {
				return nil, e
			}
			s.mutationMu.Lock()
			next, e := s.makeBenchChange(candidate, target)
			s.mutationMu.Unlock()
			if e != nil {
				return nil, e
			}
			next.token = plan.Token
			session := s.benchmark.Session()
			if session != nil {
				next.notify = func(err error) { session.SetRemoteApply(false, false, err) }
			}
			change = next
			s.benchWG.Add(1)
			go func(change *benchChange) { defer s.benchWG.Done(); change.watch(ctx) }(change)
			return true, nil
		}
		var req struct {
			Token string `json:"token"`
		}
		if json.Unmarshal(raw, &req) != nil || change == nil || change.token != req.Token {
			return nil, errors.New("preview this connection plan before applying it")
		}
		switch op {
		case "apply_commit":
			e := change.commit(inv.SourceB, inv.SourceA)
			if session := s.benchmark.Session(); session != nil {
				session.SetRemoteApply(e == nil, false, e)
			}
			return true, e
		case "apply_rollback":
			return true, change.rollback()
		case "apply_status":
			change.mu.Lock()
			defer change.mu.Unlock()
			if change.committed {
				change.expires = time.Now().Add(90 * time.Second)
			}
			t := s.manager.Telemetry(change.after.ID)
			return gin.H{"peer": change.committed && t.Peer, "committed": change.committed, "finalized": change.finalized, "pool_ready": t.PoolReady, "last_rejected_source": t.LastRejectedSource}, nil
		case "apply_finalize":
			e := change.finalize()
			if session := s.benchmark.Session(); session != nil {
				session.SetRemoteApply(false, e == nil, e)
			}
			return true, e
		}
		return nil, errors.New("unsupported apply operation")
	}
}
func (s *Server) handleBenchmarkReceiver(c *gin.Context) {
	var req struct {
		LocalIP    string `json:"local_ip"`
		PeerIP     string `json:"peer_ip"`
		Source     string `json:"source"`
		PeerSource string `json:"peer_source"`
		PortA      int    `json:"port_a"`
		PortB      int    `json:"port_b"`
		Target     uint   `json:"target_id"`
		AllowApply bool   `json:"allow_apply"`
	}
	if e := c.ShouldBindJSON(&req); e != nil {
		c.JSON(400, gin.H{"error": "invalid receiver settings"})
		return
	}
	i := benchmark.Invitation{A: req.PeerIP, B: req.LocalIP, SourceA: req.PeerSource, SourceB: req.Source, PortA: req.PortA, PortB: req.PortB, Target: req.Target}
	var version time.Time
	if req.Target != 0 {
		var inst db.TunnelInstance
		if e := s.db.First(&inst, req.Target).Error; e != nil {
			c.JSON(404, gin.H{"error": "receiver connection not found"})
			return
		}
		if inst.Mode != "remote" {
			c.JSON(400, gin.H{"error": "select the remote half on the receiving server"})
			return
		}
		if e := s.benchmarkEndpoint(inst, i.B, i.A, i.SourceB, i.SourceA); e != nil {
			c.JSON(400, gin.H{"error": e.Error()})
			return
		}
		if inst.L3Config().ReceivePort != i.PortB || inst.L3Config().SendPort != i.PortA {
			c.JSON(400, gin.H{"error": "benchmark ports must match the selected receiver connection"})
			return
		}
		version = inst.UpdatedAt
	}
	ctx, cancel := context.WithCancel(context.Background())
	code, e := s.benchmark.Receiver(i, s.receiverBenchmarkHook(ctx, req.Target, version, i, req.AllowApply))
	if e != nil {
		cancel()
		c.JSON(400, gin.H{"error": e.Error()})
		return
	}
	s.benchMu.Lock()
	if s.benchCancel != nil {
		s.benchCancel()
	}
	s.benchCancel = cancel
	s.benchDraft = nil
	s.benchPlan = nil
	s.benchMu.Unlock()
	c.JSON(201, gin.H{"code": code, "state": s.benchmark.Status()})
}
func (s *Server) handleBenchmarkStart(c *gin.Context) {
	var req benchmarkDraft
	if e := c.ShouldBindJSON(&req); e != nil {
		c.JSON(400, gin.H{"error": "invalid benchmark settings"})
		return
	}
	i, e := benchmark.Decode(req.Code)
	if e != nil {
		c.JSON(400, gin.H{"error": e.Error()})
		return
	}
	if req.Options.RateMbps < 1 || req.Options.RateMbps > 60 {
		c.JSON(400, gin.H{"error": "benchmark base rate must be 1–60 Mbps"})
		return
	}
	var version time.Time
	if req.Target != 0 {
		var inst db.TunnelInstance
		if e = s.db.First(&inst, req.Target).Error; e != nil {
			c.JSON(404, gin.H{"error": "controller connection not found"})
			return
		}
		if inst.Mode != "local" {
			c.JSON(400, gin.H{"error": "select the local half on the controlling server"})
			return
		}
		if e = s.benchmarkEndpoint(inst, i.A, i.B, i.SourceA, i.SourceB); e != nil {
			c.JSON(400, gin.H{"error": e.Error()})
			return
		}
		if inst.L3Config().ReceivePort != i.PortA || inst.L3Config().SendPort != i.PortB {
			c.JSON(400, gin.H{"error": "benchmark ports must match the selected controller connection"})
			return
		}
		version = inst.UpdatedAt
	}
	if e = s.benchmark.Controller(req.Code, req.Options, nil); e != nil {
		c.JSON(400, gin.H{"error": e.Error()})
		return
	}
	s.benchMu.Lock()
	if s.benchCancel != nil {
		s.benchCancel()
		s.benchCancel = nil
	}
	s.benchDraft = &req
	s.benchVersion = version
	s.benchPlan = nil
	s.benchMu.Unlock()
	c.JSON(202, s.benchmark.Status())
}
func (s *Server) handleBenchmarkStop(c *gin.Context) {
	s.benchmark.Stop()
	s.benchMu.Lock()
	if s.benchCancel != nil {
		s.benchCancel()
		s.benchCancel = nil
	}
	s.benchPlan = nil
	s.benchMu.Unlock()
	c.JSON(200, s.benchmark.Status())
}
func (s *Server) handleBenchmarkPreview(c *gin.Context) {
	var req struct {
		Index int `json:"index"`
	}
	if c.ShouldBindJSON(&req) != nil {
		c.JSON(400, gin.H{"error": "choose a measured carrier pair"})
		return
	}
	s.benchMu.Lock()
	defer s.benchMu.Unlock()
	session := s.benchmark.Session()
	if session == nil || s.benchDraft == nil {
		c.JSON(409, gin.H{"error": "run a benchmark first"})
		return
	}
	v := session.Status()
	if v.Status != "done" || v.Applied {
		c.JSON(409, gin.H{"error": "wait for the benchmark to finish"})
		return
	}
	var result *benchmark.Result
	for _, r := range v.Results {
		if r.Index == req.Index && r.Status == "passed" {
			copy := r
			result = &copy
		}
	}
	if result == nil {
		c.JSON(400, gin.H{"error": "choose a successful measured carrier pair"})
		return
	}
	i := session.Invitation()
	draft := s.benchDraft
	s.mutationMu.Lock()
	var local, remote db.TunnelInstance
	var e error
	if draft.Target == 0 {
		local, remote, e = buildPair(pairRequest{Name: draft.Name, Engine: "tun", TunSubnet: draft.Subnet, LocalIP: i.A, RemoteIP: i.B, LocalSpoof: i.SourceA, RemoteSpoof: i.SourceB, Listen: "127.0.0.1:5000", Forward: "127.0.0.1:51820", Upload: result.Send, Download: result.Receive, LocalPort: i.PortA, RemotePort: i.PortB})
	} else {
		e = s.db.First(&local, draft.Target).Error
		if e == nil && !local.UpdatedAt.Equal(s.benchVersion) {
			e = errors.New("controller connection changed; run a fresh benchmark")
		}
		if e == nil {
			local.SendTransport = result.Send
			local.RecvTransport = result.Receive
			cfg := local.L3Config()
			remote = db.TunnelInstance{Name: local.Name, Engine: "tun", Mode: "remote", LocalIP: i.B, ClientIP: i.A, ClientPort: cfg.ReceivePort, ListenPort: cfg.SendPort, SpoofIP: i.SourceB, PeerSpoofIP: i.SourceA, SpoofPort: 443, TunInterface: local.TunInterface, TunAddress: cfg.PeerAddress + "/30", TunPeer: localIPFromCIDR(cfg.Address), TunMTU: cfg.MTU, PSK: cfg.PSK, SendTransport: result.Receive, RecvTransport: result.Send, ForwardAddr: "127.0.0.1:51820", PoolMode: "adaptive", PoolPPS: 25, PoolActiveSize: 100, PoolRefreshSeconds: 1800, AutoPeerSync: true}
		}
	}
	if e == nil {
		e = s.validateBenchmarkConfig(local, draft.Target == 0)
	}
	s.mutationMu.Unlock()
	if e != nil {
		c.JSON(400, gin.H{"error": e.Error()})
		return
	}
	// Put the benchmark-proven source pair first in the compiled pool
	// lists, so failover ordering and the pool tab both show the tested
	// source first. The single-field fallbacks are set to the same pair.
	prioritizeBenchmarkSources(&local, i.SourceA, i.SourceB)
	prioritizeBenchmarkSources(&remote, i.SourceB, i.SourceA)
	var token [16]byte
	if _, e = rand.Read(token[:]); e != nil {
		c.JSON(500, gin.H{"error": e.Error()})
		return
	}
	plan := &benchmarkPlan{Token: hex.EncodeToString(token[:]), Index: req.Index, Local: local, Remote: remote, Expires: time.Now().Add(4 * time.Minute).Unix()}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	if _, e = session.RemoteApply(ctx, *result, "apply_prepare", plan.offer()); e != nil {
		c.JSON(409, gin.H{"error": e.Error()})
		return
	}
	s.benchPlan = plan
	// Deliberately omit credentials and full source inventories from preview.
	c.JSON(200, gin.H{"token": plan.Token, "index": plan.Index, "name": local.Name, "replace": draft.Target != 0, "target_id": draft.Target, "remote_target_id": i.Target, "send": local.SendTransport, "receive": local.RecvTransport, "subnet": local.TunAddress, "expires": plan.Expires})
}
func (s *Server) handleBenchmarkApply(c *gin.Context) {
	var req struct {
		Token string `json:"token"`
	}
	if c.ShouldBindJSON(&req) != nil {
		c.JSON(400, gin.H{"error": "invalid plan"})
		return
	}
	s.benchMu.Lock()
	defer s.benchMu.Unlock()
	plan := s.benchPlan
	session := s.benchmark.Session()
	if plan == nil || session == nil || req.Token != plan.Token || time.Now().Unix() >= plan.Expires {
		c.JSON(409, gin.H{"error": "preview expired; preview the plan again"})
		return
	}
	r, e := session.BeginApply(plan.Index)
	if e != nil {
		c.JSON(409, gin.H{"error": e.Error()})
		return
	}
	s.mutationMu.Lock()
	change, e := s.makeBenchChange(plan.Local, plan.Local.ID)
	s.mutationMu.Unlock()
	if e != nil {
		session.EndApply(e)
		c.JSON(409, gin.H{"error": e.Error()})
		return
	}
	change.token = plan.Token
	s.benchWG.Add(1)
	go func() { defer s.benchWG.Done(); s.applyBenchmark(session, r, *plan, change) }()
	c.JSON(202, session.Status())
}
func (s *Server) applyBenchmark(session *benchmark.Session, r benchmark.Result, plan benchmarkPlan, local *benchChange) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	token := gin.H{"token": plan.Token}
	var failure error
	defer func() {
		if failure != nil {
			_, remoteErr := session.RemoteApply(ctx, r, "apply_rollback", token)
			failure = errors.Join(failure, local.rollback())
			if remoteErr != nil {
				failure = errors.Join(failure, fmt.Errorf("remote rollback unconfirmed; receiver automatically restores its previous connection after its 90-second lease expires: %w", remoteErr))
			}
		}
		session.EndApply(failure)
	}()
	if _, failure = session.RemoteApply(ctx, r, "apply_commit", token); failure != nil {
		return
	}
	inv := session.Invitation()
	if failure = local.commit(inv.SourceA, inv.SourceB); failure != nil {
		return
	}
	// The peer wait used to be a fixed 35 seconds, but a cold-started
	// smart-pool tunnel must verify sources and sync its manifest on
	// both sides first; 35 seconds was too short for a large pool and
	// rolled back healthy tunnels. The wait is now 100 seconds (inside
	// the 2-minute apply context) and the local lease is renewed on
	// every poll so the watch goroutine cannot roll back mid-wait.
	deadline := time.Now().Add(100 * time.Second)
	remotePeer, remotePoolReady, remoteRejected := false, false, ""
	for time.Now().Before(deadline) {
		if session.Status().Status != "applying" {
			failure = errors.New("apply canceled")
			return
		}
		local.renew()
		body, e := session.RemoteApply(ctx, r, "apply_status", token)
		var status struct {
			Peer         bool   `json:"peer"`
			PoolReady    bool   `json:"pool_ready"`
			LastRejected string `json:"last_rejected_source"`
		}
		if e == nil && json.Unmarshal(body, &status) == nil {
			remotePeer = status.Peer
			remotePoolReady = status.PoolReady
			remoteRejected = status.LastRejected
			if status.Peer && s.manager.Telemetry(local.after.ID).Peer {
				if _, failure = session.RemoteApply(ctx, r, "apply_finalize", token); failure != nil {
					return
				}
				failure = local.finalize()
				return
			}
		}
		select {
		case <-time.After(time.Second):
		case <-ctx.Done():
			failure = ctx.Err()
			return
		}
	}
	lt := s.manager.Telemetry(local.after.ID)
	failure = fmt.Errorf("new connection did not establish an authenticated peer; restoring previous settings (local pool ready: %t, local last rejected source: %s; remote peer: %t, remote pool ready: %t, remote last rejected source: %s)", lt.PoolReady, orNone(lt.LastRejectedSource), remotePeer, remotePoolReady, orNone(remoteRejected))
}

func orNone(value string) string {
	if value == "" {
		return "none"
	}
	return value
}

func localIPFromCIDR(value string) string {
	ip, _, e := net.ParseCIDR(value)
	if e != nil {
		return ""
	}
	return ip.String()
}

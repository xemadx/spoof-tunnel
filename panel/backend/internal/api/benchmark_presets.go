package api

import (
	"fmt"
	"strings"
	"time"

	"github.com/ParsaKSH/spoof-tunnel/internal/l3"
	"github.com/ParsaKSH/spoof-tunnel/panel/internal/db"
	"github.com/ParsaKSH/spoof-tunnel/panel/internal/manager"
	"github.com/gin-gonic/gin"
)

type benchmarkPreset struct {
	ID             uint      `json:"id"`
	Name           string    `json:"name"`
	Role           string    `json:"role"`
	LocalIP        string    `json:"local_ip"`
	PeerIP         string    `json:"peer_ip"`
	Source         string    `json:"source"`
	PeerSource     string    `json:"peer_source"`
	SourceVerified bool      `json:"source_verified"`
	PeerLearned    bool      `json:"peer_learned"`
	ReceivePort    int       `json:"receive_port"`
	SendPort       int       `json:"send_port"`
	Subnet         string    `json:"subnet"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// The private engine socket contains the authenticated, effective allowlist.
// Reading it neither edits a connection nor exports the inventory to a browser.
func (s *Server) benchmarkEffectiveConfig(inst db.TunnelInstance) (db.TunnelInstance, bool) {
	status, _ := s.manager.InstanceStatus(inst.ID)
	if !inst.AutoPeerSync || status != manager.StatusRunning {
		return inst, false
	}
	peer, err := l3.PoolControl(s.manager.PoolSocket(inst.ID), l3.PoolControlRequest{Op: "peer_sources"})
	if err != nil || len(peer.Sources) == 0 || len(peer.Sources) > l3.MaxPoolSources {
		return inst, false
	}
	candidate := inst
	candidate.PeerSpoofIPList = strings.Join(peer.Sources, "\n")
	if _, err := candidate.L3Config().Sources(); err != nil {
		return inst, false
	}
	return candidate, true
}
func (s *Server) benchmarkEndpoint(inst db.TunnelInstance, local, peer, source, peerSource string) error {
	effective, _ := s.benchmarkEffectiveConfig(inst)
	return benchmarkEndpoint(effective, local, peer, source, peerSource)
}
func (s *Server) handleBenchmarkPreset(c *gin.Context) {
	inst, ok := s.poolInstance(c)
	if !ok {
		return
	}
	inst, learned := s.benchmarkEffectiveConfig(inst)
	cfg := inst.L3Config()
	sources, err := cfg.Sources()
	if err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	peers, err := cfg.PeerSources()
	if err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	if len(sources) == 0 || len(peers) == 0 {
		c.JSON(409, gin.H{"error": "benchmark_source_pool_empty"})
		return
	}
	p := benchmarkPreset{ID: inst.ID, Name: inst.Name, Role: inst.Mode, LocalIP: cfg.LocalIP, PeerIP: cfg.PeerIP, Source: sources[0].String(), PeerSource: peers[0].String(), PeerLearned: learned, ReceivePort: cfg.ReceivePort, SendPort: cfg.SendPort, Subnet: inst.TunAddress, UpdatedAt: inst.UpdatedAt}
	if containsSource(sources, cfg.SpoofSource) {
		p.Source = cfg.SpoofSource
	}
	if containsSource(peers, cfg.PeerSpoof) {
		p.PeerSource = cfg.PeerSpoof
	}
	status, _ := s.manager.InstanceStatus(inst.ID)
	if status == manager.StatusRunning {
		view, e := l3.PoolControl(s.manager.PoolSocket(inst.ID), l3.PoolControlRequest{Op: "status", Limit: 1, Filter: "selected"})
		if e == nil && view.Status != nil {
			active := view.Status.Pool.Active
			if active == "" && len(view.Status.Pool.Items) > 0 && view.Status.Pool.Items[0].Selected {
				active = view.Status.Pool.Items[0].IP
			}
			if active != "" && containsSource(sources, active) {
				p.Source = active
				p.SourceVerified = cfg.PoolPolicy.Normalized().Mode != "legacy"
			}
		}
	}
	if err := benchmarkEndpoint(inst, p.LocalIP, p.PeerIP, p.Source, p.PeerSource); err != nil {
		c.JSON(409, gin.H{"error": fmt.Sprintf("benchmark presets unavailable: %v", err)})
		return
	}
	c.JSON(200, p)
}

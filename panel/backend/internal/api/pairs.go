package api

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"github.com/ParsaKSH/spoof-tunnel/panel/internal/db"
	"github.com/gin-gonic/gin"
	"net/http"
	"net/netip"
	"strings"
	"unicode/utf8"
)

type pairRequest struct {
	AutoStart   bool   `json:"auto_start"`
	Engine      string `json:"engine"`
	TunSubnet   string `json:"tun_subnet"`
	Name        string `json:"name" binding:"required"`
	LocalIP     string `json:"local_ip" binding:"required"`
	RemoteIP    string `json:"remote_ip" binding:"required"`
	LocalSpoof  string `json:"local_spoof" binding:"required"`
	RemoteSpoof string `json:"remote_spoof" binding:"required"`
	Listen      string `json:"listen" binding:"required"`
	Forward     string `json:"forward" binding:"required"`
	Upload      string `json:"upload" binding:"required"`
	Download    string `json:"download" binding:"required"`
	LocalPort   int    `json:"local_port" binding:"required"`
	RemotePort  int    `json:"remote_port" binding:"required"`
	SaveRole    string `json:"save_role"`
}

func buildPair(p pairRequest) (db.TunnelInstance, db.TunnelInstance, error) {
	p.Name = strings.TrimSpace(p.Name)
	if utf8.RuneCountInString(p.Name) < 1 || utf8.RuneCountInString(p.Name) > 90 {
		return db.TunnelInstance{}, db.TunnelInstance{}, fmt.Errorf("pair name must contain 1–90 characters")
	}
	if p.Engine != "" && p.Engine != "tun" && p.Engine != "udp-relay" {
		return db.TunnelInstance{}, db.TunnelInstance{}, fmt.Errorf("unknown engine")
	}
	local := db.TunnelInstance{Name: p.Name + " · local", Mode: "local", ListenAddr: p.Listen, RemoteAddr: p.RemoteIP, RemotePort: p.RemotePort, RecvPort: p.LocalPort, SpoofIP: p.LocalSpoof, SpoofPort: 443, PeerSpoofIP: p.RemoteSpoof, SendTransport: p.Upload, RecvTransport: p.Download}
	remote := db.TunnelInstance{Name: p.Name + " · remote", Mode: "remote", ListenPort: p.RemotePort, ForwardAddr: p.Forward, ClientIP: p.LocalIP, ClientPort: p.LocalPort, SpoofIP: p.RemoteSpoof, SpoofPort: 443, PeerSpoofIP: p.LocalSpoof, SendTransport: p.Download, RecvTransport: p.Upload}
	if p.Engine == "tun" {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return local, remote, err
		}
		psk := base64.StdEncoding.EncodeToString(key)
		subnet := p.TunSubnet
		if subnet == "" {
			subnet = "10.77.0.0/30"
		}
		prefix, err := netip.ParsePrefix(subnet)
		if err != nil || !prefix.Addr().Is4() || prefix.Bits() != 30 {
			return local, remote, fmt.Errorf("TUN subnet must be an IPv4 /30")
		}
		first := prefix.Masked().Addr().Next()
		second := first.Next()
		for _, c := range []*db.TunnelInstance{&local, &remote} {
			c.Engine = "tun"
			c.PoolMode = "adaptive"
			c.PoolPPS = 25
			c.PoolActiveSize = 100
			c.PoolRefreshSeconds = 1800
			c.AutoPeerSync = true
			c.Enabled = p.AutoStart
			c.TunInterface = "sp" + hex.EncodeToString(key[:3])
			c.TunMTU = 1280
			c.PSK = psk
		}
		local.LocalIP = p.LocalIP
		remote.LocalIP = p.RemoteIP
		local.TunAddress = first.String() + "/30"
		local.TunPeer = second.String()
		remote.TunAddress = second.String() + "/30"
		remote.TunPeer = first.String()
	}
	if err := local.Validate(); err != nil {
		return local, remote, fmt.Errorf("local: %w", err)
	}
	if err := remote.Validate(); err != nil {
		return local, remote, fmt.Errorf("remote: %w", err)
	}
	return local, remote, nil
}

// Generates both complementary ends; saves only the end on this host.
func (s *Server) handlePair(c *gin.Context) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	var req pairRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	local, remote, err := buildPair(req)
	if err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	var selected *db.TunnelInstance
	switch req.SaveRole {
	case "local":
		selected = &local
	case "remote":
		selected = &remote
	case "":
	default:
		c.JSON(400, gin.H{"error": "save_role must be local or remote"})
		return
	}
	if selected != nil {
		if err := db.SaveTunnel(s.db, selected, true); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"local": local, "remote": remote, "saved": selected})
}

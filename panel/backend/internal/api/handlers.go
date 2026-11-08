package api

import (
	"fmt"
	"gorm.io/gorm"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/ParsaKSH/spoof-tunnel/internal/l3"
	"github.com/ParsaKSH/spoof-tunnel/internal/tester"
	"github.com/ParsaKSH/spoof-tunnel/panel/internal/auth"
	"github.com/ParsaKSH/spoof-tunnel/panel/internal/db"
	"github.com/ParsaKSH/spoof-tunnel/panel/internal/manager"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

var dummyPasswordHash = "$2a$10$wNnQ6e8j.7ZtBkJvGzP8n.J0D4e2bZq5cZ3hWk0gT4fVq8oM4e1Gq"

func init() {
	h, err := auth.HashPassword("dummy-mitigate-timing-attacks")
	if err == nil {
		dummyPasswordHash = h
	}
}

// ── Auth Handlers ──

func (s *Server) handleAuthCheck(c *gin.Context) {
	var count int64
	s.db.Model(&db.User{}).Count(&count)
	c.JSON(http.StatusOK, gin.H{"needs_setup": count == 0})
}

func (s *Server) handleSetup(c *gin.Context) {
	c.JSON(http.StatusForbidden, gin.H{"error": "Initialize the administrator on the server with spoof-panel --setup"})
}

func (s *Server) handleLogin(c *gin.Context) {
	s.loginMu.Lock()
	now := time.Now()
	key := c.ClientIP()
	for k, entries := range s.logins {
		if len(entries) == 0 || now.Sub(entries[len(entries)-1]) > time.Minute {
			delete(s.logins, k)
		}
	}
	entries := s.logins[key]
	fresh := entries[:0]
	for _, entry := range entries {
		if now.Sub(entry) < time.Minute {
			fresh = append(fresh, entry)
		}
	}
	if len(fresh) >= 10 || len(s.logins) >= 4096 {
		s.loginMu.Unlock()
		c.Header("Retry-After", "60")
		c.JSON(429, gin.H{"error": "Too many login attempts; try again in a minute"})
		return
	}
	s.logins[key] = append(fresh, now)
	s.loginMu.Unlock()
	var req struct {
		Username string `json:"username" binding:"required"`
		Password string `json:"password" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var user db.User
	userFound := s.db.Where("username = ?", req.Username).First(&user).Error == nil
	hashToCheck := user.PasswordHash
	if !userFound || hashToCheck == "" {
		hashToCheck = dummyPasswordHash
	}
	passOk := auth.CheckPassword(req.Password, hashToCheck)
	if !userFound || !passOk {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}

	// Update last login
	s.db.Model(&user).Update("last_login", time.Now())

	token, err := auth.GenerateToken(user.ID, user.Username, user.TokenVersion)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "token generation failed"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"token": token, "username": user.Username})
}

func (s *Server) handleMe(c *gin.Context) {
	userID := c.GetUint("user_id")
	var user db.User
	s.db.First(&user, userID)
	c.JSON(http.StatusOK, user)
}

// ── Dashboard ──

func (s *Server) handleDashboard(c *gin.Context) {
	var instances []db.TunnelInstance
	s.db.Omit("spoof_ip_list", "peer_spoof_ip_list").Find(&instances)

	type instanceInfo struct {
		Recovery  manager.RecoveryState `json:"recovery"`
		Engine    string                `json:"engine"`
		Telemetry l3.Stats              `json:"telemetry"`
		ID        uint                  `json:"id"`
		Name      string                `json:"name"`
		Mode      string                `json:"mode"`
		Status    string                `json:"status"`
		Uptime    float64               `json:"uptime"`
		Error     string                `json:"error,omitempty"`
	}

	var items []instanceInfo
	var running, stopped, errored int

	for _, inst := range instances {
		inst.SpoofIPList = ""
		inst.PeerSpoofIPList = ""
		status, errMsg := s.manager.InstanceStatus(inst.ID)
		uptime := s.manager.InstanceUptime(inst.ID)
		items = append(items, instanceInfo{
			Recovery:  s.manager.RecoveryStatus(inst.ID),
			Engine:    inst.Engine,
			Telemetry: s.manager.Telemetry(inst.ID),
			ID:        inst.ID,
			Name:      inst.Name,
			Mode:      inst.Mode,
			Status:    string(status),
			Uptime:    uptime.Seconds(),
			Error:     errMsg,
		})
		switch status {
		case "running":
			running++
		case "error":
			errored++
		default:
			stopped++
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"instances":     items,
		"total":         len(instances),
		"running_count": running,
		"stopped_count": stopped,
		"error_count":   errored,
	})
}

func (s *Server) handleSystem(c *gin.Context) {
	hostname, _ := os.Hostname()

	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	// TLS state for the Settings page: whether the panel serves HTTPS,
	// for which domain, and when the certificate expires (unix seconds,
	// 0 when none). Unset when no status function was installed.
	tlsEnabled := false
	tlsDomain := ""
	var tlsExpires int64
	if s.tlsStatus != nil {
		if st := s.tlsStatus(); st.Enabled && st.Valid {
			tlsEnabled = true
			tlsDomain = st.Domain
			tlsExpires = st.Expires.Unix()
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"hostname":      hostname,
		"os":            runtime.GOOS,
		"arch":          runtime.GOARCH,
		"cpus":          runtime.NumCPU(),
		"goroutines":    runtime.NumGoroutine(),
		"memory_mb":     m.Alloc / 1024 / 1024,
		"go_version":    runtime.Version(),
		"panel_version": s.version,
		"tls_enabled":   tlsEnabled,
		"tls_domain":    tlsDomain,
		"tls_expires":   tlsExpires,
	})
}

// ── Instance CRUD ──

func (s *Server) handleListInstances(c *gin.Context) {
	var instances []db.TunnelInstance
	s.db.Omit("spoof_ip_list", "peer_spoof_ip_list").Order("id asc").Find(&instances)

	type instanceWithStatus struct {
		db.TunnelInstance
		Recovery manager.RecoveryState `json:"recovery"`
		Status   string                `json:"status"`
		Uptime   float64               `json:"uptime"`
		Error    string                `json:"status_error,omitempty"`
	}

	var result []instanceWithStatus
	for _, inst := range instances {
		inst.SpoofIPList = ""
		inst.PeerSpoofIPList = ""
		status, errMsg := s.manager.InstanceStatus(inst.ID)
		uptime := s.manager.InstanceUptime(inst.ID)
		result = append(result, instanceWithStatus{
			Recovery:       s.manager.RecoveryStatus(inst.ID),
			TunnelInstance: inst,
			Status:         string(status),
			Uptime:         uptime.Seconds(),
			Error:          errMsg,
		})
	}

	if result == nil {
		result = []instanceWithStatus{}
	}

	c.JSON(http.StatusOK, result)
}

func (s *Server) handleCreateInstance(c *gin.Context) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	var inst db.TunnelInstance
	if err := c.ShouldBindJSON(&inst); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if inst.Name == "" {
		inst.Name = "Tunnel"
	}
	if inst.Mode == "" {
		inst.Mode = "local"
	}
	if inst.SendTransport == "" {
		inst.SendTransport = "tcp"
	}
	if inst.RecvTransport == "" {
		inst.RecvTransport = "udp"
	}
	if inst.SpoofPort == 0 {
		inst.SpoofPort = 443
	}
	if inst.ListenAddr == "" {
		inst.ListenAddr = "127.0.0.1:5000"
	}
	if inst.RemotePort == 0 {
		inst.RemotePort = 8090
	}
	if inst.RecvPort == 0 {
		inst.RecvPort = 5001
	}
	if inst.ListenPort == 0 {
		inst.ListenPort = 8090
	}
	if inst.ForwardAddr == "" {
		inst.ForwardAddr = "127.0.0.1:51820"
	}
	if inst.ClientPort == 0 {
		inst.ClientPort = 5001
	}

	if err := inst.Validate(); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	inst.ID = 0 // auto-increment
	inst.CreatedAt, inst.UpdatedAt = time.Time{}, time.Time{}
	if err := db.SaveTunnel(s.db, &inst, true); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, inst)
}

func (s *Server) handleGetInstance(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}

	var inst db.TunnelInstance
	query := s.db
	if c.Query("compact") == "1" {
		query = query.Omit("spoof_ip_list", "peer_spoof_ip_list")
	}
	if err := query.First(&inst, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "instance not found"})
		return
	}

	if c.Query("compact") == "1" {
		inst.SpoofIPList = ""
		inst.PeerSpoofIPList = ""
	}
	status, errMsg := s.manager.InstanceStatus(id)
	uptime := s.manager.InstanceUptime(id)

	c.JSON(http.StatusOK, gin.H{
		"instance":  inst,
		"status":    string(status),
		"uptime":    uptime.Seconds(),
		"telemetry": s.manager.Telemetry(id),
		"recovery":  s.manager.RecoveryStatus(id),
		"error":     errMsg,
	})
}

func (s *Server) handleUpdateInstance(c *gin.Context) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	id, err := parseID(c)
	if err != nil {
		return
	}

	var existing db.TunnelInstance
	if err := s.db.First(&existing, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "instance not found"})
		return
	}

	status, _ := s.manager.InstanceStatus(id)
	if status == "running" || status == "starting" || status == "stopping" {
		c.JSON(409, gin.H{"error": "Stop the tunnel before editing its configuration"})
		return
	}
	version := existing.UpdatedAt
	oldPool := existing.SpoofIPList
	if err := c.ShouldBindJSON(&existing); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if !existing.UpdatedAt.Equal(version) {
		c.JSON(409, gin.H{"error": "Configuration changed on the server; reload before saving"})
		return
	}
	if oldPool != existing.SpoofIPList {
		var managed int64
		s.db.Model(&db.PoolFile{}).Where("instance_id = ?", id).Count(&managed)
		if managed > 0 {
			c.JSON(409, gin.H{"error": "this pool is managed by named files; use pool selection"})
			return
		}
	}

	existing.ID = id
	if err := existing.Validate(); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	if err := s.manager.StopInstance(id); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	if err := db.SaveTunnel(s.db, &existing, false); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	if c.Query("compact") == "1" {
		existing.SpoofIPList = ""
		existing.PeerSpoofIPList = ""
	}
	c.JSON(http.StatusOK, existing)
}

func (s *Server) handleDeleteInstance(c *gin.Context) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	id, err := parseID(c)
	if err != nil {
		return
	}

	// Stop if running
	s.manager.RemoveInstance(id)
	_ = s.db.Where("instance_id = ?", id).Delete(&db.ForwardRule{}).Error
	_ = s.db.Where("instance_id = ?", id).Delete(&db.PoolFile{}).Error

	if err := s.db.Delete(&db.TunnelInstance{}, id).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ── Instance Control ──

func (s *Server) handleInstanceStart(c *gin.Context) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	id, err := parseID(c)
	if err != nil {
		return
	}
	if err := s.manager.StartInstance(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "started"})
}

func (s *Server) handleInstanceStop(c *gin.Context) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	id, err := parseID(c)
	if err != nil {
		return
	}
	if err := s.manager.StopInstance(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "stopped"})
}

func (s *Server) handleInstanceRestart(c *gin.Context) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	id, err := parseID(c)
	if err != nil {
		return
	}
	if err := s.manager.RestartInstance(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "restarted"})
}

func (s *Server) handleInstanceStatus(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	status, errMsg := s.manager.InstanceStatus(id)
	c.JSON(http.StatusOK, gin.H{
		"status":    string(status),
		"error":     errMsg,
		"uptime":    s.manager.InstanceUptime(id).Seconds(),
		"telemetry": s.manager.Telemetry(id),
		"recovery":  s.manager.RecoveryStatus(id),
	})
}

// WebSocket log streaming per instance
var upgrader = websocket.Upgrader{Subprotocols: []string{"spoof-logs"}}

func (s *Server) handleInstanceLogs(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	conn.SetReadLimit(1024)

	// Subscribe at the exact history boundary before writing its snapshot.
	history, subID, logCh := s.manager.FollowLogs(id, 100)
	defer s.manager.UnsubscribeLogs(id, subID)
	for _, line := range history {
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if err := conn.WriteMessage(websocket.TextMessage, []byte(line)); err != nil {
			return
		}
	}

	// Detect client disconnect via a read goroutine
	done := make(chan struct{})
	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				close(done)
				return
			}
		}
	}()

	// Stream new logs
	for {
		select {
		case line, ok := <-logCh:
			if !ok {
				return
			}
			_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.WriteMessage(websocket.TextMessage, []byte(line)); err != nil {
				return
			}
		case <-done:
			return
		}
	}
}

// ── Instance Spoof IPs ──

func (s *Server) handleGetInstanceSpoofIPs(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}

	var inst db.TunnelInstance
	if err := s.db.First(&inst, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "instance not found"})
		return
	}

	content := strings.TrimSpace(inst.SpoofIPList)
	count := 0
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			count++
		}
	}

	c.JSON(http.StatusOK, gin.H{"content": content, "count": count})
}

func (s *Server) handleSetInstanceSpoofIPs(c *gin.Context) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	id, err := parseID(c)
	if err != nil {
		return
	}

	var managed int64
	s.db.Model(&db.PoolFile{}).Where("instance_id = ? AND kind <> ? AND enabled = ?", id, "default", true).Count(&managed)
	if managed > 0 {
		c.JSON(409, gin.H{"error": "this pool is managed by named files; use pool selection"})
		return
	}

	var req struct {
		Content string `json:"content"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	content := strings.TrimSpace(req.Content)
	// Validate here, not only at tunnel start: the L3 engine accepts
	// one unicast IPv4 per line. CIDR prefixes and dash ranges are
	// rejected with a clear error instead of being saved and then
	// failing cryptically when the tunnel starts (the old UI text
	// wrongly claimed they were supported).
	count := 0
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		ip := net.ParseIP(line)
		if ip == nil || ip.To4() == nil || ip.IsUnspecified() || ip.IsMulticast() {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("spoof IP %q must be a single unicast IPv4 address (CIDR and dash ranges are not supported here)", line)})
			return
		}
		count++
	}
	var inst db.TunnelInstance
	if err := s.db.First(&inst, id).Error; err != nil {
		c.JSON(404, gin.H{"error": "instance not found"})
		return
	}
	status, _ := s.manager.InstanceStatus(id)
	if status == "running" || status == "starting" || status == "stopping" {
		c.JSON(409, gin.H{"error": "Stop the tunnel before editing its configuration"})
		return
	}
	inst.SpoofIPList = content
	if err := s.manager.StopInstance(id); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := db.SaveTunnel(tx, &inst, false); err != nil {
			return err
		}
		if inst.Engine == "tun" {
			content := inst.SpoofIPList
			if strings.TrimSpace(content) == "" {
				content = inst.SpoofIP
			}
			ips, err := l3.ParseSourceText(strings.NewReader(content))
			if err != nil {
				return err
			}
			var row db.PoolFile
			if err = tx.Where("instance_id = ? AND kind = ?", inst.ID, "default").First(&row).Error; err != nil {
				return err
			}
			value := poolFile("پیش‌فرض", inst.ID, ips)
			row.Data = value.Data
			row.Hash = value.Hash
			row.Count = len(ips)
			row.Enabled = true
			return tx.Save(&row).Error
		}
		return nil
	}); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"ok": true, "count": count, "instance": inst})
}

// ── Tester Handlers ──

func (s *Server) handleTesterStart(c *gin.Context) {
	var req struct {
		Mode          string  `json:"mode" binding:"required"`
		TestID        string  `json:"test_id"`
		Protocol      string  `json:"protocol" binding:"required"`
		IPList        string  `json:"ip_list"`
		FilePath      string  `json:"file_path"`
		DstIP         string  `json:"dst_ip"`
		DstPort       int     `json:"dst_port"`
		Timeout       int     `json:"timeout"`
		PacketCount   int     `json:"packet_count"`
		MaxPacketLoss float64 `json:"max_packet_loss"`
		Concurrency   int     `json:"concurrency"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var ranges *tester.IPRangeSet
	var err error

	if req.FilePath != "" {
		dataDir := ""
		if s.manager != nil {
			dataDir = s.manager.DataDir()
		}
		if dataDir == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "file_path is disabled (no data directory configured)"})
			return
		}
		cleanPath := filepath.Clean(req.FilePath)
		if !filepath.IsAbs(cleanPath) {
			cleanPath = filepath.Join(dataDir, cleanPath)
		}
		realDataDir, errRealDir := filepath.EvalSymlinks(dataDir)
		if errRealDir != nil {
			realDataDir = dataDir
		}
		realPath, errReal := filepath.EvalSymlinks(cleanPath)
		if errReal != nil {
			realPath = cleanPath
		}
		rel, errRel := filepath.Rel(realDataDir, realPath)
		if errRel != nil || strings.HasPrefix(rel, "..") || rel == "." || strings.Contains(cleanPath, "..") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "file_path must be located within the panel data directory"})
			return
		}
		f, errFile := os.Open(realPath)
		if errFile != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "cannot open file path"})
			return
		}
		defer f.Close()
		ranges, err = tester.ParseIPRangesFromReader(f)
	} else if req.IPList != "" {
		ranges, err = tester.ParseIPRangesFromString(req.IPList)
	} else {
		c.JSON(http.StatusBadRequest, gin.H{"error": "either ip_list or file_path must be provided"})
		return
	}

	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid IP list: " + err.Error()})
		return
	}

	if ranges.Total() == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "IP list is empty"})
		return
	}

	maxTotalPackets := int64(100000)
	if ranges.Total() > 50000 || ranges.Total()*int64(req.PacketCount) > maxTotalPackets {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("total packet budget exceeded (max %d packets across at most 50,000 IPs)", maxTotalPackets)})
		return
	}

	// Validate synchronously here, not inside the background goroutine:
	// without this, a sender with an unknown protocol silently finished
	// with zero packets sent, and an invalid destination only surfaced
	// later as an async status error.
	if req.Protocol != "tcp" && req.Protocol != "icmp" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "protocol must be 'tcp' or 'icmp'"})
		return
	}
	if req.Mode == "sender" {
		parsedDst := net.ParseIP(req.DstIP)
		if parsedDst == nil || parsedDst.To4() == nil || parsedDst.IsMulticast() || parsedDst.IsUnspecified() {
			c.JSON(http.StatusBadRequest, gin.H{"error": "dst_ip must be a valid IPv4 address"})
			return
		}
	}
	if req.PacketCount < 1 || req.PacketCount > 1000 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "packet_count must be 1–1000"})
		return
	}
	if req.Timeout < 1 || req.Timeout > 3600 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "timeout must be 1–3600 seconds"})
		return
	}
	if req.MaxPacketLoss < 0 || req.MaxPacketLoss > 100 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "max_packet_loss must be 0–100"})
		return
	}

	if (req.Mode == "sender" || req.TestID != "") && !tester.ValidTestID(req.TestID) {
		c.JSON(400, gin.H{"error": "copy the 32-character test code from the receiver"})
		return
	}
	cfg := tester.TesterConfig{
		TestID:        req.TestID,
		Mode:          req.Mode,
		Protocol:      req.Protocol,
		DstIP:         req.DstIP,
		DstPort:       req.DstPort,
		Timeout:       req.Timeout,
		PacketCount:   req.PacketCount,
		MaxPacketLoss: req.MaxPacketLoss,
		Concurrency:   req.Concurrency,
	}

	switch req.Mode {
	case "sender":
		if err := s.tester.RunSender(cfg, ranges); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	case "receiver":
		if err := s.tester.RunReceiver(cfg, ranges); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "mode must be 'sender' or 'receiver'"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "started", "ip_count": ranges.Total()})
}

func (s *Server) handleTesterStatus(c *gin.Context) {
	state := s.tester.State()
	// Load results: live during running receiver, from disk when done
	if state.Status == "done" || state.Mode == "receiver" {
		results, err := s.tester.ResultsLimit(200)
		if err == nil {
			state.Results = results
		}
	}
	c.JSON(http.StatusOK, gin.H{"status": state.Status, "mode": state.Mode, "error": state.Error, "progress": state.Progress, "total_ips": state.TotalIPs, "processed_ips": state.ProcessedIPs, "sent_packets": state.SentPackets, "failed_packets": state.FailedPackets, "passed_count": state.PassedCount, "recv_count": state.RecvCount, "results": state.Results, "preview_limit": 200, "test_id": state.TestID, "rejected_probes": state.RejectedProbes, "duplicate_probes": state.DuplicateProbes})
}

func (s *Server) handleTesterStop(c *gin.Context) {
	s.tester.Stop()
	c.JSON(http.StatusOK, gin.H{"status": "stopped"})
}

func (s *Server) handleTesterResults(c *gin.Context) {
	state := s.tester.State()
	results, _ := s.tester.Results()
	c.JSON(http.StatusOK, gin.H{
		"status":  state.Status,
		"results": results,
	})
}

func (s *Server) handleTesterDownload(c *gin.Context) {
	passedIPs, err := s.tester.PassedIPs()
	if err != nil || len(passedIPs) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "no results"})
		return
	}

	content := strings.Join(passedIPs, "\n") + "\n"
	c.Header("Content-Disposition", "attachment; filename=spoof-ips.txt")
	c.Data(http.StatusOK, "text/plain", []byte(content))
}

func (s *Server) handleTesterUpload(c *gin.Context) {
	file, _, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no file uploaded"})
		return
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "read file: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"content": string(data)})
}

// ── Settings ──

func (s *Server) handleChangePassword(c *gin.Context) {
	userID := c.GetUint("user_id")
	var req struct {
		OldPassword string `json:"old_password" binding:"required"`
		NewPassword string `json:"new_password" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var user db.User
	s.db.First(&user, userID)

	if !auth.CheckPassword(req.OldPassword, user.PasswordHash) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "wrong password"})
		return
	}

	if len(req.NewPassword) < 12 || len(req.NewPassword) > 72 {
		c.JSON(400, gin.H{"error": "Password must be 12–72 bytes"})
		return
	}
	if err := db.ChangePassword(s.db, &user, req.NewPassword); err != nil {
		c.JSON(500, gin.H{"error": "password update failed"})
		return
	}
	// Generate new token
	token, _ := auth.GenerateToken(user.ID, user.Username, user.TokenVersion)
	c.JSON(http.StatusOK, gin.H{"ok": true, "token": token})
}

// ── Helpers ──

func parseID(c *gin.Context) (uint, error) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil || id == 0 {
		if err == nil {
			err = fmt.Errorf("invalid id")
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return 0, err
	}
	return uint(id), nil
}

func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

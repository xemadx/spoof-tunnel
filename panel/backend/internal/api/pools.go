package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ParsaKSH/spoof-tunnel/internal/l3"
	"github.com/ParsaKSH/spoof-tunnel/panel/internal/db"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func (s *Server) poolInstance(c *gin.Context) (db.TunnelInstance, bool) {
	var inst db.TunnelInstance
	id, err := parseID(c)
	if err != nil {
		return inst, false
	}
	if s.db.First(&inst, id).Error != nil {
		c.JSON(404, gin.H{"error": "instance not found"})
		return inst, false
	}
	if inst.Engine != "tun" {
		c.JSON(400, gin.H{"error": "smart pools require the TUN engine"})
		return inst, false
	}
	return inst, true
}
func poolFile(name string, inst uint, ips []net.IP) db.PoolFile {
	data := l3.EncodeSourceFile(ips)
	sum := sha256.Sum256(data)
	return db.PoolFile{InstanceID: inst, Kind: "file", Name: name, Data: data, Count: len(ips), Hash: hex.EncodeToString(sum[:])}
}
func (s *Server) poolMetadata(id uint) ([]db.PoolFile, error) {
	rows := []db.PoolFile{}
	err := s.db.Select("id, instance_id, kind, name, hash, count, enabled, position, created_at").Where("instance_id = ?", id).Order("position asc, id asc").Find(&rows).Error
	return rows, err
}
func (s *Server) handlePoolList(c *gin.Context) {
	inst, ok := s.poolInstance(c)
	if !ok {
		return
	}
	files, err := s.poolMetadata(inst.ID)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	total := len(strings.Fields(inst.SpoofIPList))
	if total == 0 && inst.SpoofIP != "" {
		total = 1
	}
	peerCount := len(strings.Fields(inst.PeerSpoofIPList))
	if peerCount == 0 && inst.PeerSpoofIP != "" {
		peerCount = 1
	}
	c.JSON(200, gin.H{"files": files, "mode": l3.PoolPolicy{Mode: inst.PoolMode}.Normalized().Mode, "probe_pps": l3.PoolPolicy{ProbePPS: inst.PoolPPS}.Normalized().ProbePPS, "active_size": inst.PoolActiveSize, "refresh_seconds": l3.PoolPolicy{RefreshSeconds: inst.PoolRefreshSeconds}.Normalized().RefreshSeconds, "auto_peer_sync": inst.AutoPeerSync, "total": total, "peer_count": peerCount, "legacy_content": len(files) == 0 && strings.TrimSpace(inst.SpoofIPList) != "", "updated_at": inst.UpdatedAt})
}
func (s *Server) handlePoolUpload(c *gin.Context) {
	inst, ok := s.poolInstance(c)
	if !ok {
		return
	}
	// The multipart body has a separate bounded upload allowance. It never
	// raises the JSON settings limit or accepts a server-local file path.
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(400, gin.H{"error": "choose a TXT file"})
		return
	}
	defer file.Close()
	name := strings.TrimSpace(filepath.Base(strings.ReplaceAll(header.Filename, "\\", "/")))
	if strings.ToLower(filepath.Ext(name)) != ".txt" || utf8.RuneCountInString(name) > 140 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		c.JSON(400, gin.H{"error": "a TXT filename up to 140 characters is required"})
		return
	}
	ips, err := l3.ParseSourceText(io.LimitReader(file, 12<<20))
	if err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	// Read and validate the bounded upload before acquiring the global
	// configuration lock, so slow clients cannot block Start/Stop.
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	inst, ok = s.poolInstance(c)
	if !ok {
		return
	}
	var count int64
	s.db.Model(&db.PoolFile{}).Where("instance_id = ? AND kind <> ?", inst.ID, "default").Count(&count)
	if count >= 64 {
		c.JSON(400, gin.H{"error": "at most 64 pool files per connection"})
		return
	}
	base := strings.TrimSuffix(name, filepath.Ext(name))
	for suffix := 1; suffix <= 65; suffix++ {
		var same int64
		s.db.Model(&db.PoolFile{}).Where("instance_id = ? AND name = ?", inst.ID, name).Count(&same)
		if same == 0 {
			break
		}
		ending := fmt.Sprintf(" (%d).txt", suffix+1)
		runes := []rune(base)
		if len(runes) > 140-utf8.RuneCountInString(ending) {
			runes = runes[:140-utf8.RuneCountInString(ending)]
		}
		name = string(runes) + ending
	}
	row := poolFile(name, inst.ID, ips)
	row.Position = int(count)
	var duplicate int64
	s.db.Model(&db.PoolFile{}).Where("instance_id = ? AND kind <> ? AND hash = ?", inst.ID, "default", row.Hash).Count(&duplicate)
	if duplicate > 0 {
		c.JSON(409, gin.H{"error": "this address file is already imported"})
		return
	}
	err = s.db.Create(&row).Error
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(201, row)
}
func (s *Server) handlePoolMigrate(c *gin.Context) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	inst, ok := s.poolInstance(c)
	if !ok {
		return
	}
	if err := db.EnsureDefaultPool(s.db, &inst); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	var row db.PoolFile
	if s.db.Where("instance_id = ? AND kind = ?", inst.ID, "default").First(&row).Error != nil {
		c.JSON(404, gin.H{"error": "default group not found"})
		return
	}
	c.JSON(200, row)
}

type poolSelection struct {
	ActiveSize     int       `json:"active_size"`
	RefreshSeconds int       `json:"refresh_seconds"`
	IDs            []uint    `json:"ids"`
	Mode           string    `json:"mode"`
	PPS            int       `json:"probe_pps"`
	AutoSync       bool      `json:"auto_peer_sync"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func savePoolSelection(tx *gorm.DB, inst *db.TunnelInstance, req poolSelection) error {
	if len(req.IDs) > 65 || len(req.IDs) == 0 {
		return fmt.Errorf("select between 1 and 65 non-empty groups")
	}
	policy := l3.PoolPolicy{Mode: req.Mode, ProbePPS: req.PPS, ActiveSize: req.ActiveSize, RefreshSeconds: req.RefreshSeconds}.Normalized()
	if err := policy.Validate(); err != nil {
		return err
	}
	ips := []string{}
	seen := map[string]bool{}
	idSeen := map[uint]bool{}
	for _, id := range req.IDs {
		if idSeen[id] {
			return fmt.Errorf("duplicate pool file selection")
		}
		idSeen[id] = true
		var row db.PoolFile
		if tx.Where("instance_id = ?", inst.ID).First(&row, id).Error != nil {
			return fmt.Errorf("pool file does not belong to this connection")
		}
		if row.Count == 0 {
			return fmt.Errorf("an empty default group cannot be selected")
		}
		values, err := l3.DecodeSourceFile(row.Data)
		if err != nil {
			return err
		}
		for _, ip := range values {
			v := ip.String()
			if !seen[v] {
				seen[v] = true
				ips = append(ips, v)
				if len(ips) > l3.MaxPoolSources {
					return fmt.Errorf("selected union exceeds the source limit")
				}
			}
		}
	}
	inst.SpoofIPList = strings.Join(ips, "\n")
	inst.PoolMode = policy.Mode
	inst.PoolPPS = policy.ProbePPS
	inst.PoolActiveSize = policy.ActiveSize
	inst.PoolRefreshSeconds = policy.RefreshSeconds
	inst.AutoPeerSync = req.AutoSync
	if err := inst.Validate(); err != nil {
		return err
	}

	if err := tx.Model(&db.PoolFile{}).Where("instance_id = ?", inst.ID).Update("enabled", false).Error; err != nil {
		return err
	}
	for position, id := range req.IDs {
		if err := tx.Model(&db.PoolFile{}).Where("id = ? AND instance_id = ?", id, inst.ID).Updates(map[string]any{"enabled": true, "position": position}).Error; err != nil {
			return err
		}
	}
	return db.SaveTunnel(tx, inst, false)
}

func (s *Server) handlePoolSettings(c *gin.Context) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	inst, ok := s.poolInstance(c)
	if !ok {
		return
	}
	status, _ := s.manager.InstanceStatus(inst.ID)
	if status == "running" || status == "starting" || status == "stopping" {
		c.JSON(409, gin.H{"error": "stop this connection before applying its pool selection"})
		return
	}
	var req poolSelection
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	if !req.UpdatedAt.Equal(inst.UpdatedAt) {
		c.JSON(409, gin.H{"error": "connection changed; reload its pool settings"})
		return
	}
	err := s.db.Transaction(func(tx *gorm.DB) error { return savePoolSelection(tx, &inst, req) })
	if err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"ok": true, "total": effectiveSourceCount(inst), "updated_at": inst.UpdatedAt})
}
func (s *Server) handlePoolDelete(c *gin.Context) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	inst, ok := s.poolInstance(c)
	if !ok {
		return
	}
	id, err := strconv.ParseUint(c.Param("fileID"), 10, 32)
	if err != nil {
		c.JSON(400, gin.H{"error": "invalid file id"})
		return
	}
	var row db.PoolFile
	if s.db.Where("instance_id = ?", inst.ID).First(&row, id).Error != nil {
		c.JSON(404, gin.H{"error": "pool file not found"})
		return
	}
	if row.Kind == "default" {
		c.JSON(409, gin.H{"error": "the default group cannot be deleted"})
		return
	}

	if row.Enabled {
		c.JSON(409, gin.H{"error": "unselect and apply this file before deleting it"})
		return
	}
	if err = s.db.Delete(&row).Error; err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"ok": true})
}
func (s *Server) handlePoolState(c *gin.Context) {
	var inst db.TunnelInstance
	id, err := parseID(c)
	if err != nil {
		return
	}
	if s.db.Select("id,engine,psk,peer_pool_revision,updated_at").First(&inst, id).Error != nil {
		c.JSON(404, gin.H{"error": "instance not found"})
		return
	}
	if inst.Engine != "tun" {
		c.JSON(400, gin.H{"error": "smart pools require the TUN engine"})
		return
	}
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	filter := c.Query("state")
	response, err := l3.PoolControl(s.manager.PoolSocket(inst.ID), l3.PoolControlRequest{Op: "status", Offset: offset, Limit: limit, Filter: filter})
	if err != nil {
		c.JSON(200, gin.H{"available": false, "reason": "connection engine is not running or does not support pool telemetry"})
		return
	}
	// Persist a newly authenticated peer revision once, without transferring its
	// large address list to the browser or embedding it in every settings PUT.
	if response.Status != nil && response.Status.Peer.Status == "synced" && response.Status.Peer.Revision != inst.PeerPoolRevision {
		peer, err := l3.PoolControl(s.manager.PoolSocket(inst.ID), l3.PoolControlRequest{Op: "peer_sources"})
		if err == nil && len(peer.Sources) > 0 {
			text := strings.Join(peer.Sources, "\n")
			{
				s.mutationMu.Lock()
				var current db.TunnelInstance
				if s.db.First(&current, inst.ID).Error == nil && current.PSK == inst.PSK && current.UpdatedAt.Equal(inst.UpdatedAt) {
					current.PeerSpoofIPList = text
					if current.Validate() == nil {
						_ = s.db.Model(&current).UpdateColumns(map[string]any{"peer_spoof_ip_list": text, "peer_pool_revision": response.Status.Peer.Revision}).Error
					}
				}
				s.mutationMu.Unlock()
			}
		}
	}
	c.JSON(200, gin.H{"available": true, "runtime": response.Status})
}
func (s *Server) handlePoolSync(c *gin.Context) {
	inst, ok := s.poolInstance(c)
	if !ok {
		return
	}
	_, err := l3.PoolControl(s.manager.PoolSocket(inst.ID), l3.PoolControlRequest{Op: "sync"})
	if err != nil {
		c.JSON(409, gin.H{"error": "start the connection with an updated engine before requesting peer sources"})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"ok": true, "status": "waiting"})
}
func (s *Server) handlePoolDownload(c *gin.Context) {
	inst, ok := s.poolInstance(c)
	if !ok {
		return
	}
	id, err := strconv.ParseUint(c.Param("fileID"), 10, 32)
	if err != nil {
		c.JSON(400, gin.H{"error": "invalid file id"})
		return
	}
	var row db.PoolFile
	if s.db.Where("instance_id = ?", inst.ID).First(&row, id).Error != nil {
		c.JSON(404, gin.H{"error": "pool file not found"})
		return
	}
	ips, err := l3.DecodeSourceFile(row.Data)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	values := make([]string, 0, len(ips))
	for _, ip := range ips {
		values = append(values, ip.String())
	}
	sort.Strings(values)
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=pool-%d.txt", row.ID))
	c.Data(200, "text/plain; charset=utf-8", []byte(strings.Join(values, "\n")+"\n"))
}

func effectiveSourceCount(inst db.TunnelInstance) int {
	values, err := inst.L3Config().Sources()
	if err != nil {
		return 0
	}
	return len(values)
}

func (s *Server) handlePoolSyncCancel(c *gin.Context) {
	inst, ok := s.poolInstance(c)
	if !ok {
		return
	}
	_, err := l3.PoolControl(s.manager.PoolSocket(inst.ID), l3.PoolControlRequest{Op: "cancel_sync"})
	if err != nil {
		c.JSON(409, gin.H{"error": "connection engine is unavailable"})
		return
	}
	c.JSON(200, gin.H{"ok": true})
}

func (s *Server) handleDefaultPoolGet(c *gin.Context) {
	inst, ok := s.poolInstance(c)
	if !ok {
		return
	}
	var row db.PoolFile
	if s.db.Where("instance_id = ? AND kind = ?", inst.ID, "default").First(&row).Error != nil {
		c.JSON(404, gin.H{"error": "default group not found"})
		return
	}
	values := []string{}
	if row.Count > 0 {
		ips, err := l3.DecodeSourceFile(row.Data)
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		for _, ip := range ips {
			values = append(values, ip.String())
		}
	}
	c.JSON(200, gin.H{"content": strings.Join(values, "\n"), "count": row.Count, "updated_at": inst.UpdatedAt})
}
func (s *Server) handleDefaultPoolSave(c *gin.Context) {
	inst, ok := s.poolInstance(c)
	if !ok {
		return
	}
	status, _ := s.manager.InstanceStatus(inst.ID)
	if status == "running" || status == "starting" || status == "stopping" {
		c.JSON(409, gin.H{"error": "stop this connection before editing the default group"})
		return
	}
	file, _, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(400, gin.H{"error": "manual source content is required"})
		return
	}
	defer file.Close()
	version, err := time.Parse(time.RFC3339Nano, c.Request.FormValue("updated_at"))
	if err != nil {
		c.JSON(409, gin.H{"error": "connection changed; reload its pool settings"})
		return
	}
	var selection *poolSelection
	if value := c.Request.FormValue("settings"); value != "" {
		selection = &poolSelection{}
		if len(value) > 16384 || json.Unmarshal([]byte(value), selection) != nil {
			c.JSON(400, gin.H{"error": "invalid pool selection"})
			return
		}
	}
	raw, err := io.ReadAll(io.LimitReader(file, (10<<20)+1))
	if err != nil || len(raw) > 10<<20 {
		c.JSON(400, gin.H{"error": "manual source content exceeds 10 MiB"})
		return
	}
	var ips []net.IP
	if strings.TrimSpace(string(raw)) != "" {
		ips, err = l3.ParseSourceText(strings.NewReader(string(raw)))
		if err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	inst, ok = s.poolInstance(c)
	if !ok {
		return
	}
	status, _ = s.manager.InstanceStatus(inst.ID)
	if status == "running" || status == "starting" || status == "stopping" {
		c.JSON(409, gin.H{"error": "stop this connection before editing the default group"})
		return
	}
	if !version.Equal(inst.UpdatedAt) {
		c.JSON(409, gin.H{"error": "connection changed; reload its pool settings"})
		return
	}
	var row db.PoolFile
	if s.db.Where("instance_id = ? AND kind = ?", inst.ID, "default").First(&row).Error != nil {
		c.JSON(404, gin.H{"error": "default group not found"})
		return
	}
	encoded := poolFile("پیش‌فرض", inst.ID, ips)
	row.Data, row.Hash, row.Count = encoded.Data, encoded.Hash, len(ips)
	if len(ips) == 0 {
		row.Enabled = false
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if selection != nil {
			if err := tx.Save(&row).Error; err != nil {
				return err
			}
			// Clearing the default also unselects it, in the same transaction.
			if row.Count == 0 {
				ids := []uint{}
				for _, id := range selection.IDs {
					if id != row.ID {
						ids = append(ids, id)
					}
				}
				selection.IDs = ids
			}
			return savePoolSelection(tx, &inst, *selection)
		}
		if !row.Enabled && row.Count > 0 {
			var active int64
			if err := tx.Model(&db.PoolFile{}).Where("instance_id = ? AND enabled = ? AND id <> ?", inst.ID, true, row.ID).Count(&active).Error; err != nil {
				return err
			}
			if active == 0 {
				row.Enabled = true
			}
		}
		if err := tx.Save(&row).Error; err != nil {
			return err
		}
		var active []db.PoolFile
		if err := tx.Where("instance_id = ? AND enabled = ?", inst.ID, true).Order("position asc,id asc").Find(&active).Error; err != nil {
			return err
		}
		if len(active) == 0 {
			return fmt.Errorf("keep at least one non-empty group selected before clearing the default group")
		}
		joined := []string{}
		seen := map[string]bool{}
		for _, f := range active {
			values, err := l3.DecodeSourceFile(f.Data)
			if err != nil {
				return err
			}
			for _, ip := range values {
				v := ip.String()
				if !seen[v] {
					seen[v] = true
					joined = append(joined, v)
					if len(joined) > l3.MaxPoolSources {
						return fmt.Errorf("selected union exceeds the source limit")
					}
				}
			}
		}
		inst.SpoofIPList = strings.Join(joined, "\n")
		return db.SaveTunnel(tx, &inst, false)
	})
	if err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"ok": true, "count": row.Count, "total": effectiveSourceCount(inst), "updated_at": inst.UpdatedAt})
}

package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/ParsaKSH/spoof-tunnel/panel/internal/db"
	"github.com/ParsaKSH/spoof-tunnel/panel/internal/manager"
	"github.com/gin-gonic/gin"
)

type logEventJSON struct {
	Time       string           `json:"time"`
	InstanceID uint             `json:"instance_id"`
	Instance   string           `json:"instance"`
	Level      manager.LogLevel `json:"level"`
	Message    string           `json:"message"`
}

// handleGlobalLogs returns the panel-wide console: the newest entries
// across all tunnels, oldest first, with tunnel names resolved from the
// database. The ring is in memory and starts empty on every panel start,
// so entries only cover the time since the panel last started.
func (s *Server) handleGlobalLogs(c *gin.Context) {
	limit := 200
	if raw := c.Query("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}
	level := c.Query("level")
	events := s.manager.Events(limit)
	names := map[uint]string{}
	var all []db.TunnelInstance
	if err := s.db.Find(&all).Error; err == nil {
		for _, inst := range all {
			names[inst.ID] = inst.Name
		}
	}
	out := make([]logEventJSON, 0, len(events))
	for _, e := range events {
		if level != "" && string(e.Level) != level {
			continue
		}
		out = append(out, logEventJSON{
			Time:       e.Time.Format(time.RFC3339),
			InstanceID: e.InstanceID,
			Instance:   names[e.InstanceID],
			Level:      e.Level,
			Message:    e.Message,
		})
	}
	c.JSON(http.StatusOK, gin.H{"events": out})
}

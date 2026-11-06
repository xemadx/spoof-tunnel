package api

import (
	"encoding/json"
	"fmt"
	"log"

	"github.com/ParsaKSH/spoof-tunnel/panel/internal/db"
	"gorm.io/gorm"
)

// The private SQLite journal and candidate config commit in one transaction.
// Recovery runs before main auto-starts enabled tunnels, including after SIGKILL.
type benchmarkJournal struct {
	Before    db.TunnelInstance `json:"before"`
	After     db.TunnelInstance `json:"after"`
	Existed   bool              `json:"existed"`
	Committed bool              `json:"committed"`
	Running   bool              `json:"running"`
}

func (b *benchChange) journalKey() string { return "benchmark-apply-" + b.token }
func (b *benchChange) journal(tx *gorm.DB, committed bool) error {
	if b.token == "" {
		return fmt.Errorf("missing benchmark transaction token")
	}
	data, e := json.Marshal(benchmarkJournal{Before: b.before, After: b.after, Existed: b.existed, Committed: committed, Running: b.running})
	if e != nil {
		return e
	}
	return tx.Save(&db.Setting{Key: b.journalKey(), Value: string(data)}).Error
}
func (b *benchChange) clearJournal(tx *gorm.DB) error {
	return tx.Where("key = ?", b.journalKey()).Delete(&db.Setting{}).Error
}
func (b *benchChange) finalize() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.committed || !b.s.manager.Telemetry(b.after.ID).Peer {
		return fmt.Errorf("new connection has no authenticated peer")
	}
	if e := b.clearJournal(b.s.db); e != nil {
		return e
	}
	b.finalized = true
	return nil
}
func (s *Server) recoverBenchmarkChanges() {
	var rows []db.Setting
	if e := s.db.Where("key LIKE ?", "benchmark-apply-%").Find(&rows).Error; e != nil {
		log.Printf("benchmark recovery: %v", e)
		return
	}
	for _, row := range rows {
		var resumeID uint
		e := s.db.Transaction(func(tx *gorm.DB) error {
			var j benchmarkJournal
			if e := json.Unmarshal([]byte(row.Value), &j); e != nil {
				return e
			}
			if j.Committed {
				var latest db.TunnelInstance
				if e := tx.First(&latest, j.After.ID).Error; e != nil {
					return e
				}
				if !latest.UpdatedAt.Equal(j.After.UpdatedAt) {
					return fmt.Errorf("connection %d changed after an interrupted apply; preserving the newer edit", j.After.ID)
				}
				if j.Existed {
					previous := j.Before
					if e := db.SaveTunnel(tx, &previous, false); e != nil {
						return e
					}
				} else {
					if e := tx.Where("instance_id = ?", j.After.ID).Delete(&db.ForwardRule{}).Error; e != nil {
						return e
					}
					if e := tx.Where("instance_id = ?", j.After.ID).Delete(&db.PoolFile{}).Error; e != nil {
						return e
					}
					if e := tx.Delete(&db.TunnelInstance{}, j.After.ID).Error; e != nil {
						return e
					}
				}
			}
			if !j.Committed && j.Existed {
				var latest db.TunnelInstance
				if e := tx.First(&latest, j.Before.ID).Error; e != nil {
					return e
				}
				if !latest.UpdatedAt.Equal(j.Before.UpdatedAt) {
					return fmt.Errorf("connection %d changed after apply intent; preserving newer edit", j.Before.ID)
				}
			}
			if j.Existed && j.Running && !j.Before.Enabled {
				resumeID = j.Before.ID
			}
			return tx.Where("key = ?", row.Key).Delete(&db.Setting{}).Error
		})
		if e != nil {
			log.Printf("benchmark recovery: %v", e)
		} else {
			log.Printf("recovered interrupted benchmark apply")
			if resumeID != 0 {
				if err := s.manager.StartInstance(resumeID); err != nil {
					log.Printf("benchmark recovery could not restart connection %d: %v", resumeID, err)
				}
			}
		}
	}
}

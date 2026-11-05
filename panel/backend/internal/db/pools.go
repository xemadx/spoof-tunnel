package db

import (
	"crypto/sha256"
	"encoding/hex"
	"github.com/ParsaKSH/spoof-tunnel/internal/l3"
	"gorm.io/gorm"
	"strings"
)

// EnsureDefaultPool is an idempotent migration: a legacy transmit set becomes
// the manual default group only when no named groups already exist.
func EnsureDefaultPool(database *gorm.DB, c *TunnelInstance) error {
	if c.Engine != "tun" {
		return nil
	}
	var count int64
	if err := database.Model(&PoolFile{}).Where("instance_id = ? AND kind = ?", c.ID, "default").Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	row := PoolFile{InstanceID: c.ID, Kind: "default", Name: "پیش‌فرض", Position: -1}
	if err := database.Model(&PoolFile{}).Where("instance_id = ?", c.ID).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		content := c.SpoofIPList
		if strings.TrimSpace(content) == "" {
			content = c.SpoofIP
		}
		if ips, err := l3.ParseSourceText(strings.NewReader(content)); err == nil {
			row.Data = l3.EncodeSourceFile(ips)
			row.Count = len(ips)
			row.Enabled = true
		}
	}
	sum := sha256.Sum256(row.Data)
	row.Hash = hex.EncodeToString(sum[:])
	return database.Create(&row).Error
}

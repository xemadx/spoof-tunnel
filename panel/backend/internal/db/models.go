package db

import (
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// User represents an admin user
type User struct {
	TokenVersion uint      `json:"-"`
	ID           uint      `gorm:"primarykey" json:"id"`
	Username     string    `gorm:"uniqueIndex;not null" json:"username"`
	PasswordHash string    `gorm:"not null" json:"-"`
	CreatedAt    time.Time `json:"created_at"`
	LastLogin    time.Time `json:"last_login"`
}

// TunnelInstance represents a single tunnel configuration + runtime
type TunnelInstance struct {
	ID            uint   `gorm:"primarykey" json:"id"`
	Name          string `gorm:"not null" json:"name"`
	Enabled       bool   `gorm:"default:false" json:"enabled"`
	Mode          string `gorm:"default:local" json:"mode"`
	SendTransport string `gorm:"default:tcp" json:"send_transport"`
	RecvTransport string `gorm:"default:udp" json:"recv_transport"`

	Engine       string `gorm:"default:udp-relay" json:"engine"`
	LocalIP      string `json:"local_ip"`
	TunInterface string `json:"tun_interface"`
	TunAddress   string `json:"tun_address"`
	TunPeer      string `json:"tun_peer"`
	TunMTU       int    `json:"tun_mtu"`
	PSK          string `json:"psk"`

	// Local mode
	ListenAddr string `gorm:"default:127.0.0.1:5000" json:"listen_addr"`
	RemoteAddr string `json:"remote_addr"`
	RemotePort int    `gorm:"default:8090" json:"remote_port"`
	RecvPort   int    `gorm:"default:5001" json:"recv_port"`

	// Remote mode
	ListenPort  int    `gorm:"default:8090" json:"listen_port"`
	ForwardAddr string `gorm:"default:127.0.0.1:51820" json:"forward_addr"`
	ClientIP    string `json:"client_ip"`
	ClientPort  int    `gorm:"default:5001" json:"client_port"`

	// Spoof
	PeerPoolRevision   string `json:"peer_pool_revision"`
	PoolMode           string `json:"pool_mode"`
	PoolPPS            int    `json:"pool_pps"`
	PoolActiveSize     int    `json:"pool_active_size"`
	PoolRefreshSeconds int    `json:"pool_refresh_seconds"`
	AutoPeerSync       bool   `json:"auto_peer_sync"`
	SpoofIP            string `json:"spoof_ip"`
	SpoofPort          int    `gorm:"default:443" json:"spoof_port"`
	PeerSpoofIP        string `json:"peer_spoof_ip"`
	PeerSpoofIPList    string `gorm:"type:text" json:"peer_spoof_ip_list"`
	SpoofIPList        string `gorm:"type:text" json:"spoof_ip_list"` // inline IPs, newline-separated

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ForwardRule is one reverse-tunnel entry point attached to a TUN tunnel.
//
// Semantics (see package forward): on the entry server (typically Iran)
// the panel listens on ListenPort for end-user clients and pipes each
// TCP connection to the tunnel peer's TUN address (TunPeer) on
// TargetPort, where the real service (e.g. 3x-ui / Xray) listens.
// Target IP is deliberately NOT stored: it is always the owning
// instance's TunPeer, so a rule can never drift into proxying to an
// arbitrary public address.
//
// v1 is TCP only. Protocol is stored so a future UDP implementation can
// reuse the table without a migration, but any non-"tcp" value is
// rejected by the API until that implementation exists.
type ForwardRule struct {
	ID         uint   `gorm:"primarykey" json:"id"`
	InstanceID uint   `gorm:"index;not null" json:"instance_id"`
	Name       string `gorm:"not null" json:"name"`
	Protocol   string `gorm:"default:tcp" json:"protocol"`
	ListenPort int    `gorm:"not null" json:"listen_port"`
	TargetPort int    `gorm:"not null" json:"target_port"`
	Enabled    bool   `json:"enabled"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TrafficStat stores traffic snapshots
type TrafficStat struct {
	ID            uint      `gorm:"primarykey" json:"id"`
	BytesSent     int64     `gorm:"default:0" json:"bytes_sent"`
	BytesReceived int64     `gorm:"default:0" json:"bytes_received"`
	RecordedAt    time.Time `gorm:"autoCreateTime" json:"recorded_at"`
}

// Setting stores key-value settings
type Setting struct {
	Key   string `gorm:"primarykey" json:"key"`
	Value string `json:"value"`
}

// InitDB opens the SQLite database and runs migrations
func InitDB(dbPath string) (*gorm.DB, error) {
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, err
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(1)
	for _, statement := range []string{"PRAGMA busy_timeout=5000", "PRAGMA secure_delete=ON"} {
		if err := db.Exec(statement).Error; err != nil {
			_ = sqlDB.Close()
			return nil, err
		}
	}

	// Auto-migrate all models
	if err := db.AutoMigrate(
		&User{},
		&TunnelInstance{},
		&ForwardRule{},
		&PoolFile{},
		&TrafficStat{},
		&Setting{},
	); err != nil {
		return nil, err
	}

	// Remove the legacy recoverable password copy. The bcrypt hash is retained.
	if err := db.Where("key = ?", "admin_password").Delete(&Setting{}).Error; err != nil {
		return nil, err
	}

	// Migrate old ServerConfig → TunnelInstance (one-time)
	migrateOldConfig(db)
	if err := db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS pool_default_per_instance ON pool_files(instance_id) WHERE kind = 'default'").Error; err != nil {
		return nil, err
	}
	var tunnels []TunnelInstance
	if err := db.Where("engine = ?", "tun").Find(&tunnels).Error; err != nil {
		return nil, err
	}
	for i := range tunnels {
		if err := EnsureDefaultPool(db, &tunnels[i]); err != nil {
			return nil, err
		}
	}

	return db, nil
}

// migrateOldConfig converts legacy single ServerConfig to TunnelInstance
func migrateOldConfig(database *gorm.DB) {
	// Check if old server_configs table exists
	if !database.Migrator().HasTable("server_configs") {
		return
	}

	// Check if we already migrated
	var count int64
	database.Model(&TunnelInstance{}).Count(&count)
	if count > 0 {
		return
	}

	// Read old config
	type OldConfig struct {
		ID            uint
		Mode          string
		SendTransport string
		RecvTransport string
		ListenAddr    string
		RemoteAddr    string
		RemotePort    int
		RecvPort      int
		ListenPort    int
		ForwardAddr   string
		ClientIP      string
		ClientPort    int
		SpoofIP       string
		SpoofPort     int
		PeerSpoofIP   string
	}

	var old OldConfig
	if err := database.Table("server_configs").First(&old).Error; err != nil {
		return
	}

	// Create TunnelInstance from old config
	instance := TunnelInstance{
		Name:          "Default Tunnel",
		Enabled:       false,
		Mode:          old.Mode,
		SendTransport: old.SendTransport,
		RecvTransport: old.RecvTransport,
		ListenAddr:    old.ListenAddr,
		RemoteAddr:    old.RemoteAddr,
		RemotePort:    old.RemotePort,
		RecvPort:      old.RecvPort,
		ListenPort:    old.ListenPort,
		ForwardAddr:   old.ForwardAddr,
		ClientIP:      old.ClientIP,
		ClientPort:    old.ClientPort,
		SpoofIP:       old.SpoofIP,
		SpoofPort:     old.SpoofPort,
		PeerSpoofIP:   old.PeerSpoofIP,
	}
	database.Create(&instance)
}

// PoolFile stores compact IPv4 addresses; API responses expose metadata only.
type PoolFile struct {
	Kind       string    `gorm:"default:file" json:"kind"`
	ID         uint      `gorm:"primarykey" json:"id"`
	InstanceID uint      `gorm:"index;not null" json:"instance_id"`
	Name       string    `json:"name"`
	Hash       string    `json:"hash"`
	Count      int       `json:"count"`
	Enabled    bool      `json:"enabled"`
	Position   int       `json:"position"`
	Data       []byte    `gorm:"type:blob" json:"-"`
	CreatedAt  time.Time `json:"created_at"`
}

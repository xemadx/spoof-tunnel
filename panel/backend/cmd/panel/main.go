package main

import (
	"context"
	"embed"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ParsaKSH/spoof-tunnel/panel/internal/api"
	"github.com/ParsaKSH/spoof-tunnel/panel/internal/auth"
	"github.com/ParsaKSH/spoof-tunnel/panel/internal/db"
	"github.com/ParsaKSH/spoof-tunnel/panel/internal/manager"
	"github.com/ParsaKSH/spoof-tunnel/panel/internal/updater"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

//go:embed all:web
var webFS embed.FS

// panelVersion is the panel release version shown in the CLI banner,
// in --version output and, via /api/system, in the panel header.
// It is set at build time with:
//
//	go build -ldflags "-X main.panelVersion=vX.Y.Z" ./cmd/panel
//
// and defaults to "dev" for a plain local build, so a build that was
// not stamped with a release can never masquerade as one.
var panelVersion = "dev"

func main() {
	bind := flag.String("bind", "0.0.0.0", "panel bind IPv4 address")
	port := flag.Int("port", 0, "panel port (0 = auto from DB or random)")
	setup := flag.Bool("setup", false, "run initial setup with random credentials")
	setupUser := flag.String("setup-user", "", "setup: admin username")
	setupPass := flag.String("setup-pass", "", "setup: admin password")
	setupPort := flag.Int("setup-port", 0, "setup: panel port")
	showVersion := flag.Bool("version", false, "print panel version and exit")
	applyUpdate := flag.Bool("apply-update", false, "apply a queued update from the independent systemd service")
	flag.Parse()

	if *showVersion {
		fmt.Println(panelVersion)
		return
	}

	if *applyUpdate {
		signalCtx, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stopSignals()
		ctx, cancel := context.WithTimeout(signalCtx, 15*time.Minute)
		defer cancel()
		if err := updater.New(panelVersion, "/etc/spoof-panel").Apply(ctx); err != nil {
			log.Fatal(err)
		}
		return
	}
	// Determine data directory
	dataDir := "/etc/spoof-panel"
	if os.Getenv("SPOOF_DATA_DIR") != "" {
		dataDir = os.Getenv("SPOOF_DATA_DIR")
	}
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		log.Fatal(err)
	}
	_ = os.Chmod(dataDir, 0700)

	dbPath := filepath.Join(dataDir, "panel.db")
	isSetup := *setup || *setupUser != ""
	if !isSetup {
		log.Printf("Database: %s", dbPath)
	}

	// Init database
	database, err := db.InitDB(dbPath)
	if err != nil {
		log.Fatalf("Failed to init database: %v", err)
	}

	auth.SetSecret(getOrCreateSetting(database, "jwt_secret", func() string { return auth.GenerateRandomString(64) }))
	_ = os.Chmod(dbPath, 0600)
	credsPath := filepath.Join(dataDir, "credentials.txt")
	if data, err := os.ReadFile(credsPath); err == nil {
		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			if strings.HasPrefix(line, "Password:") {
				lines[i] = "Password: not stored; use --setup --setup-pass to reset"
			}
		}
		if err := os.WriteFile(credsPath, []byte(strings.Join(lines, "\n")), 0600); err != nil {
			log.Fatal(err)
		}
	}
	// Handle setup mode
	if isSetup {
		runSetup(database, *setupUser, *setupPass, *setupPort, dataDir)
		return
	}

	// Determine spoof binary path
	binaryPath := filepath.Join(dataDir, "spoof")
	if _, err := os.Stat(binaryPath); os.IsNotExist(err) {
		if p, err := exec.LookPath("spoof"); err == nil {
			binaryPath = p
		}
	}

	// Determine web path (random secret URL prefix)
	webPath := getOrCreateSetting(database, "web_path", func() string {
		return auth.GenerateRandomString(10)
	})

	// Create tunnel manager
	mgr := manager.NewManager(database, binaryPath, dataDir)

	// Create API server — API is prefixed with /{webpath}/api
	srv := api.NewServer(database, mgr, "/"+webPath, panelVersion)

	// Auto-start enabled tunnels
	var tunnels []db.TunnelInstance
	if err := database.Where("enabled = ?", true).Find(&tunnels).Error; err == nil {
		for _, t := range tunnels {
			log.Printf("Auto-starting tunnel: %s", t.Name)
			mgr.StartInstance(t.ID)
		}
	}

	// Serve embedded frontend
	webRoot, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatalf("embedded web: %v", err)
	}

	// Custom file server
	serveFile := func(c *gin.Context, filePath string) bool {
		f, err := webRoot.Open(filePath)
		if err != nil {
			return false
		}
		defer f.Close()

		stat, err := f.Stat()
		if err != nil || stat.IsDir() {
			return false
		}

		ext := filepath.Ext(filePath)
		contentType := ""
		switch ext {
		case ".html":
			contentType = "text/html; charset=utf-8"
		case ".css":
			contentType = "text/css; charset=utf-8"
		case ".js":
			contentType = "application/javascript; charset=utf-8"
		case ".json":
			contentType = "application/json"
		case ".png":
			contentType = "image/png"
		case ".svg":
			contentType = "image/svg+xml"
		case ".ico":
			contentType = "image/x-icon"
		case ".woff2":
			contentType = "font/woff2"
		case ".woff":
			contentType = "font/woff"
		case ".txt":
			contentType = "text/plain"
		case ".map":
			contentType = "application/json"
		default:
			contentType = "application/octet-stream"
		}

		data, err := io.ReadAll(f)
		if err != nil {
			return false
		}

		/* Speed fix: the custom file server sent no Cache-Control at
		   all, so every full page reload re-downloaded every hashed
		   _next chunk and font. Hashed _next/static files are immutable
		   by construction, so cache them for a year; HTML stays
		   no-cache so a new panel binary is picked up immediately. */
		if strings.HasPrefix(filePath, "_next/static/") {
			c.Header("Cache-Control", "public, max-age=31536000, immutable")
		} else if ext == ".html" {
			c.Header("Cache-Control", "no-cache")
		}

		c.Data(http.StatusOK, contentType, data)
		return true
	}

	// All routes require /{webpath}/ prefix
	prefix := "/" + webPath
	srv.Router().NoRoute(func(c *gin.Context) {
		path := c.Request.URL.Path

		// Root → redirect to /{webpath}/
		if path == "/" || path == "" {
			c.Redirect(http.StatusFound, prefix+"/")
			return
		}

		// Static-export metadata requests the favicon at the origin root.
		if path == "/favicon.ico" && serveFile(c, "favicon.ico") {
			return
		}

		// Allow /_next/static assets from root (Next.js hardcodes these paths)
		if strings.HasPrefix(path, "/_next/") {
			filePath := path[1:] // remove leading /
			if serveFile(c, filePath) {
				return
			}
		}

		// Must start with /{webpath}
		if path != prefix && !strings.HasPrefix(path, prefix+"/") {
			c.String(http.StatusNotFound, "not found")
			return
		}

		// Strip prefix to get the actual file/page path
		relPath := strings.TrimPrefix(path, prefix)
		relPath = strings.TrimPrefix(relPath, "/")

		if relPath != "" {
			if serveFile(c, relPath) {
				return
			}
			if serveFile(c, relPath+"/index.html") {
				return
			}
			cleaned := strings.TrimSuffix(relPath, "/")
			if cleaned != relPath {
				if serveFile(c, cleaned) {
					return
				}
				if serveFile(c, cleaned+"/index.html") {
					return
				}
			}
		}

		// SPA fallback
		serveFile(c, "index.html")
	})

	// Determine port
	listenPort := *port
	if listenPort == 0 {
		var setting db.Setting
		if err := database.Where("key = ?", "panel_port").First(&setting).Error; err == nil {
			fmt.Sscanf(setting.Value, "%d", &listenPort)
		}
	}
	if listenPort == 0 {
		listenPort = auth.GenerateRandomPort()
		database.Create(&db.Setting{Key: "panel_port", Value: fmt.Sprintf("%d", listenPort)})
	}

	// Graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("Shutting down...")
		srv.Close()
		mgr.StopAll()
		os.Exit(0)
	}()

	addr := net.JoinHostPort(*bind, fmt.Sprint(listenPort))
	ip := detectServerIP()
	fullURL := fmt.Sprintf("http://%s:%d/%s/", ip, listenPort, webPath)
	log.Printf("╔══════════════════════════════════════════════════════════════════════╗")
	log.Printf("║  %-68s║", "Spoof Tunnel "+panelVersion)
	log.Printf("║  %-68s║", fullURL)
	log.Printf("╚══════════════════════════════════════════════════════════════════════╝")

	httpServer := &http.Server{Addr: addr, Handler: srv.Router(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}
	if err := httpServer.ListenAndServe(); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

// detectServerIP attempts to determine the external public IPv4 of this host.
func detectServerIP() string {
	if value := os.Getenv("SPOOF_PUBLIC_IP"); net.ParseIP(value).To4() != nil {
		return value
	}
	endpoints := []string{
		"https://api.ipify.org",
		"https://ifconfig.me/ip",
		"https://icanhazip.com",
	}
	client := &http.Client{Timeout: 3 * time.Second}
	for _, ep := range endpoints {
		resp, err := client.Get(ep)
		if err == nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
			resp.Body.Close()
			candidate := strings.TrimSpace(string(body))
			if parsed := net.ParseIP(candidate); parsed != nil && parsed.To4() != nil {
				return candidate
			}
		}
	}
	addrs, err := net.InterfaceAddrs()
	if err == nil {
		for _, addr := range addrs {
			if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() && ipnet.IP.To4() != nil {
				return ipnet.IP.String()
			}
		}
	}
	return "YOUR_IP"
}

// getOrCreateSetting reads a setting from DB, or creates it with a default value.
// Using Limit(1).Find avoids noisy "record not found" logs on a fresh database.
func getOrCreateSetting(database *gorm.DB, key string, defaultFn func() string) string {
	var settings []db.Setting
	if err := database.Where("key = ?", key).Limit(1).Find(&settings).Error; err == nil && len(settings) > 0 {
		return settings[0].Value
	}
	value := defaultFn()
	database.Create(&db.Setting{Key: key, Value: value})
	return value
}

func runSetup(database *gorm.DB, username, password string, port int, dataDir string) {
	if port < 0 || port > 65535 {
		log.Fatal("Port must be 1–65535 (or 0 for automatic)")
	}
	if password != "" && (len(password) < 12 || len(password) > 72) {
		log.Fatal("Password must be 12–72 bytes")
	}
	username = strings.TrimSpace(username)
	if len(username) > 100 {
		log.Fatal("Username must be at most 100 bytes")
	}
	var count int64
	database.Model(&db.User{}).Count(&count)

	var user db.User
	var webPath string

	if count > 0 {
		database.First(&user)
		if username != "" && username != user.Username {
			user.Username = username
			database.Save(&user)
		} else {
			username = user.Username
		}

		// Retrieve or update port
		var portSetting db.Setting
		if port == 0 {
			if err := database.Where("key = ?", "panel_port").First(&portSetting).Error; err == nil {
				fmt.Sscanf(portSetting.Value, "%d", &port)
			}
			if port == 0 {
				port = auth.GenerateRandomPort()
				database.Save(&db.Setting{Key: "panel_port", Value: fmt.Sprintf("%d", port)})
			}
		} else {
			database.Save(&db.Setting{Key: "panel_port", Value: fmt.Sprintf("%d", port)})
		}

		// Retrieve web path
		webPath = getOrCreateSetting(database, "web_path", func() string {
			return auth.GenerateRandomString(10)
		})

		if password != "" {
			if err := db.ChangePassword(database, &user, password); err != nil {
				log.Fatal(err)
			}
		}

	} else {
		// First install
		if password != "" && (len(password) < 12 || len(password) > 72) {
			log.Fatal("Password must be 12–72 bytes")
		}
		if username == "" {
			username = auth.GenerateRandomString(8)
		}
		if password == "" {
			password = auth.GenerateRandomString(16)
		}
		if port == 0 {
			port = auth.GenerateRandomPort()
		}

		hash, err := auth.HashPassword(password)
		if err != nil {
			log.Fatal(err)
		}
		if err := database.Create(&db.User{
			Username:     username,
			PasswordHash: hash,
		}).Error; err != nil {
			log.Fatal(err)
		}

		database.Save(&db.Setting{Key: "panel_port", Value: fmt.Sprintf("%d", port)})

		webPath = getOrCreateSetting(database, "web_path", func() string {
			return auth.GenerateRandomString(10)
		})
	}

	ip := detectServerIP()
	panelURL := fmt.Sprintf("http://%s:%d/%s/", ip, port, webPath)

	// Keep connection information for root, never the plaintext password.
	credsPath := filepath.Join(dataDir, "credentials.txt")
	credsContent := fmt.Sprintf("URL: %s\nUsername: %s\nPort: %d\nWeb Path: /%s\nPassword: not stored; reset with spoof-panel --setup --setup-pass NEW_PASSWORD\n", panelURL, username, port, webPath)
	if err := os.WriteFile(credsPath, []byte(credsContent), 0600); err != nil {
		log.Fatal(err)
	}
	if password == "" {
		password = "unchanged (not stored)"
	}

	fmt.Println()
	fmt.Println("╔══════════════════════════════════════════════════════════════════════╗")
	fmt.Println("║                    Spoof Panel — Credentials                         ║")
	fmt.Println("╠══════════════════════════════════════════════════════════════════════╣")
	fmt.Printf("║  Port:     %-58d║\n", port)
	fmt.Printf("║  Username: %-58s║\n", username)
	fmt.Printf("║  Password: %-58s║\n", password)
	fmt.Printf("║  Web Path: %-58s║\n", "/"+webPath)
	fmt.Println("╠══════════════════════════════════════════════════════════════════════╣")
	fmt.Printf("║  URL:      %-58s║\n", panelURL)
	fmt.Println("╚══════════════════════════════════════════════════════════════════════╝")
	fmt.Println()
}

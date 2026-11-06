package main

import (
	"context"
	"crypto/tls"
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
	"github.com/ParsaKSH/spoof-tunnel/panel/internal/tlsconf"
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
	setTLS := flag.Bool("set-tls", false, "enable TLS with --tls-domain/--tls-cert/--tls-key and exit")
	tlsDomain := flag.String("tls-domain", "", "set-tls: fully-qualified domain the certificate covers")
	tlsCert := flag.String("tls-cert", "", "set-tls: certificate file (full chain, e.g. /etc/letsencrypt/live/<domain>/fullchain.pem)")
	tlsKey := flag.String("tls-key", "", "set-tls: private key file (e.g. /etc/letsencrypt/live/<domain>/privkey.pem)")
	tlsStatusFlag := flag.Bool("tls-status", false, "print TLS status (key=value lines) and exit")
	disableTLS := flag.Bool("disable-tls", false, "disable TLS and exit (panel serves plain HTTP on next start)")
	flag.Parse()

	if *showVersion {
		fmt.Println(panelVersion)
		return
	}

	// Determine data directory (needed by the TLS admin commands below).
	dataDir := "/etc/spoof-panel"
	if os.Getenv("SPOOF_DATA_DIR") != "" {
		dataDir = os.Getenv("SPOOF_DATA_DIR")
	}

	// TLS administration: these touch only tls.json, never the database
	// or a running panel, so they run before any init.
	if *setTLS {
		runSetTLS(dataDir, *tlsDomain, *tlsCert, *tlsKey)
		return
	}
	if *disableTLS {
		cfg, _ := tlsconf.Load(dataDir)
		cfg.Enabled = false
		if err := tlsconf.Save(dataDir, cfg); err != nil {
			log.Fatal(err)
		}
		fmt.Println("TLS disabled; the panel will serve plain HTTP on next start")
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
	// Determine data directory (already resolved above for the TLS admin
	// commands); ensure it exists with root-only permissions.
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		log.Fatal(err)
	}
	_ = os.Chmod(dataDir, 0700)

	// Recover an update interrupted by a kill or power loss: complete it
	// from the staged binaries or restore the previous pair, before any
	// tunnel starts with a mixed pair. Reconcile returns true when the
	// on-disk binaries changed and the process must restart to load
	// them; the panel unit restarts on failure, so exit non-zero.
	if restart, err := updater.New(panelVersion, dataDir).Reconcile(); err != nil {
		log.Printf("interrupted-update recovery: %v", err)
	} else if restart {
		log.Printf("interrupted-update recovery: binaries reconciled, exiting so the service restarts")
		os.Exit(1)
	}

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

	// TLS status report for the installer: needs the database for the
	// panel port and web path, so it runs after init and before serving.
	if *tlsStatusFlag {
		printTLSStatus(database, dataDir)
		return
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
	srv.SetTLSStatus(func() tlsconf.Status { return tlsconf.Evaluate(dataDir) })

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

	// TLS: serve HTTPS when a valid certificate is configured. A
	// configured-but-broken certificate never bricks the panel: it logs
	// loudly and falls back to plain HTTP, and the installer can fix it
	// by re-running. The loader re-reads the files on every handshake,
	// so a certbot renewal takes effect with no restart.
	scheme := "http"
	var tlsLoader *tlsconf.Loader
	tlsSt := tlsconf.Evaluate(dataDir)
	if tlsSt.Enabled {
		if tlsSt.Valid {
			scheme = "https"
			tlsLoader = tlsconf.NewLoader(tlsSt.CertFile, tlsSt.KeyFile)
			if time.Until(tlsSt.Expires) < 30*24*time.Hour {
				log.Printf("WARNING: TLS certificate for %s expires on %s; renew it (certbot renew) before then", tlsSt.Domain, tlsSt.Expires.Format("2006-01-02"))
			}
		} else {
			log.Printf("WARNING: TLS is configured but the certificate is not usable (%s); serving plain HTTP", tlsSt.Error)
		}
	}

	fullURL := fmt.Sprintf("%s://%s:%d/%s/", scheme, ip, listenPort, webPath)
	log.Printf("╔══════════════════════════════════════════════════════════════════════╗")
	log.Printf("║  %-68s║", "Spoof Tunnel "+panelVersion)
	log.Printf("║  %-68s║", fullURL)
	log.Printf("╚══════════════════════════════════════════════════════════════════════╝")

	httpServer := &http.Server{Addr: addr, Handler: srv.Router(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}
	if tlsLoader != nil {
		httpServer.TLSConfig = &tls.Config{GetCertificate: tlsLoader.GetCertificate, MinVersion: tls.VersionTLS12}
		// Empty cert/key names: with GetCertificate set, Go serves the
		// loader's certificate instead of loading files here.
		if err := httpServer.ListenAndServeTLS("", ""); err != nil {
			log.Fatalf("Server failed: %v", err)
		}
	} else if err := httpServer.ListenAndServe(); err != nil {
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
	scheme := "http"
	if st := tlsconf.Evaluate(dataDir); st.Enabled && st.Valid {
		scheme = "https"
	}
	panelURL := fmt.Sprintf("%s://%s:%d/%s/", scheme, ip, port, webPath)

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

// runSetTLS validates a certificate/key pair for one domain and enables
// TLS for the panel. It refuses, loudly, a pair that does not load, does
// not match, is out of date, or does not cover the domain — a bad TLS
// configuration must fail here, at setup time, never silently at serve
// time.
func runSetTLS(dataDir, domain, certFile, keyFile string) {
	domain = tlsconf.NormalizeDomain(domain)
	if !tlsconf.ValidDomain(domain) {
		log.Fatalf("invalid domain %q: use a fully-qualified domain name (no wildcard)", domain)
	}
	if certFile == "" || keyFile == "" {
		log.Fatal("--set-tls needs --tls-cert and --tls-key")
	}
	expires, issuer, err := tlsconf.Validate(certFile, keyFile, domain)
	if err != nil {
		log.Fatalf("TLS certificate rejected: %v", err)
	}
	if err := tlsconf.Save(dataDir, tlsconf.Config{Enabled: true, Domain: domain, CertFile: certFile, KeyFile: keyFile}); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("TLS enabled for %s (certificate valid until %s, issuer %s)\n", domain, expires.Format(time.RFC3339), issuer)
}

// printTLSStatus reports the TLS state as key=value lines for the
// installer: enabled/domain/cert/key/valid/error/expires plus the panel
// port and web path, so the installer can verify the HTTPS endpoint
// without parsing anything else.
func printTLSStatus(database *gorm.DB, dataDir string) {
	st := tlsconf.Evaluate(dataDir)
	var port, prefix string
	var s db.Setting
	if err := database.Where("key = ?", "panel_port").First(&s).Error; err == nil {
		port = s.Value
	}
	if err := database.Where("key = ?", "web_path").First(&s).Error; err == nil {
		prefix = s.Value
	}
	expires := ""
	expiresUnix := ""
	if !st.Expires.IsZero() {
		expires = st.Expires.Format(time.RFC3339)
		expiresUnix = fmt.Sprint(st.Expires.Unix())
	}
	fmt.Printf("enabled=%t\n", st.Enabled)
	fmt.Printf("domain=%s\n", st.Domain)
	fmt.Printf("cert_file=%s\n", st.CertFile)
	fmt.Printf("key_file=%s\n", st.KeyFile)
	fmt.Printf("valid=%t\n", st.Valid)
	fmt.Printf("error=%s\n", st.Error)
	fmt.Printf("expires=%s\n", expires)
	fmt.Printf("expires_unix=%s\n", expiresUnix)
	fmt.Printf("port=%s\n", port)
	fmt.Printf("web_path=%s\n", prefix)
}

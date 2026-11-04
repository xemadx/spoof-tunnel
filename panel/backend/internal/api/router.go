package api

import (
	"github.com/ParsaKSH/spoof-tunnel/panel/internal/db"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ParsaKSH/spoof-tunnel/internal/tester"
	"github.com/ParsaKSH/spoof-tunnel/panel/internal/auth"
	"github.com/ParsaKSH/spoof-tunnel/panel/internal/manager"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"gorm.io/gorm"
)

// Server is the API server
type Server struct {
	db         *gorm.DB
	manager    *manager.Manager
	tester     *tester.Runner
	router     *gin.Engine
	webPath    string
	version    string
	mutationMu *sync.Mutex
	loginMu    sync.Mutex
	logins     map[string][]time.Time
}

// NewServer creates a new API server. version is the panel release
// version baked in at build time (e.g. v0.2.3); it is the single source
// of truth the frontend shows, so it can never go stale like a
// hard-coded string in the frontend did.
func NewServer(database *gorm.DB, mgr *manager.Manager, webPath string, version string) *Server {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	_ = r.SetTrustedProxies(nil)
	r.Use(gin.Recovery())

	r.Use(func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 2<<20)
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("Referrer-Policy", "no-referrer")
		c.Header("X-Frame-Options", "DENY")
		c.Next()
	})

	s := &Server{
		mutationMu: mgr.MutationGuard(),
		db:         database,
		manager:    mgr,
		tester:     tester.NewRunner(),
		router:     r,
		webPath:    webPath,
		version:    version,
		logins:     make(map[string][]time.Time),
	}

	s.setupRoutes()
	return s
}

// Router returns the gin router
func (s *Server) Router() *gin.Engine {
	return s.router
}

// WebPath returns the configured web path
func (s *Server) WebPath() string {
	return s.webPath
}

func (s *Server) setupRoutes() {
	api := s.router.Group(s.webPath + "/api")
	{
		// Auth (no middleware)
		api.POST("/auth/login", s.handleLogin)
		api.POST("/auth/setup", s.handleSetup)
		api.GET("/auth/check", s.handleAuthCheck)

		// Protected routes
		protected := api.Group("")
		protected.Use(s.authMiddleware())
		{
			protected.GET("/auth/me", s.handleMe)

			// Dashboard
			protected.GET("/dashboard", s.handleDashboard)
			protected.GET("/system", s.handleSystem)

			// Tunnel Instances CRUD
			protected.POST("/pairs", s.handlePair)
			protected.GET("/instances", s.handleListInstances)
			protected.POST("/instances", s.handleCreateInstance)
			protected.GET("/instances/:id", s.handleGetInstance)
			protected.PUT("/instances/:id", s.handleUpdateInstance)
			protected.DELETE("/instances/:id", s.handleDeleteInstance)

			// Tunnel Instance Control
			protected.POST("/instances/:id/start", s.handleInstanceStart)
			protected.POST("/instances/:id/stop", s.handleInstanceStop)
			protected.POST("/instances/:id/restart", s.handleInstanceRestart)
			protected.GET("/instances/:id/status", s.handleInstanceStatus)
			protected.GET("/instances/:id/logs", s.handleInstanceLogs)

			// Instance Spoof IPs
			protected.GET("/instances/:id/spoof-ips", s.handleGetInstanceSpoofIPs)
			protected.PUT("/instances/:id/spoof-ips", s.handleSetInstanceSpoofIPs)

			// Reverse-tunnel forward rules (entry -> peer TUN service)
			protected.GET("/instances/:id/forwards", s.handleListForwards)
			protected.POST("/instances/:id/forwards", s.handleCreateForward)
			protected.DELETE("/instances/:id/forwards/:ruleID", s.handleDeleteForward)

			// Tester
			protected.POST("/tester/start", s.handleTesterStart)
			protected.GET("/tester/status", s.handleTesterStatus)
			protected.POST("/tester/stop", s.handleTesterStop)
			protected.GET("/tester/results", s.handleTesterResults)
			protected.GET("/tester/download", s.handleTesterDownload)
			protected.POST("/tester/upload", s.handleTesterUpload)

			// Settings
			protected.PUT("/settings/password", s.handleChangePassword)
		}
	}
}

// authMiddleware validates JWT tokens
func (s *Server) authMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		var tokenStr string

		// Check Authorization header first
		header := c.GetHeader("Authorization")
		if header != "" {
			tokenStr = strings.TrimPrefix(header, "Bearer ")
		} else if websocket.IsWebSocketUpgrade(c.Request) {
			protocols := websocket.Subprotocols(c.Request)
			if len(protocols) == 2 && protocols[0] == "spoof-logs" {
				tokenStr = protocols[1]
			}
		}

		if tokenStr == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "no token"})
			return
		}

		claims, err := auth.ValidateToken(tokenStr)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
			return
		}

		var user db.User
		if s.db.Select("id", "token_version").First(&user, claims.UserID).Error != nil || user.TokenVersion != claims.TokenVersion {
			c.AbortWithStatusJSON(401, gin.H{"error": "session expired"})
			return
		}
		c.Set("user_id", claims.UserID)
		c.Set("username", claims.Username)
		c.Next()
	}
}

// Close joins the diagnostic worker before application shutdown.
func (s *Server) Close() { s.tester.Stop(); s.manager.StopAll() }

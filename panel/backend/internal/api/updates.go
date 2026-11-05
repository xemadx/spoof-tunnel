package api

import "github.com/gin-gonic/gin"

func (s *Server) handleUpdateStatus(c *gin.Context) { c.JSON(200, s.updates.Status()) }
func (s *Server) handleUpdateCheck(c *gin.Context) {
	status, err := s.updates.Check(c.Request.Context())
	if err != nil {
		c.JSON(502, gin.H{"error": err.Error(), "status": status})
		return
	}
	c.JSON(200, status)
}
func (s *Server) handleUpdateInstall(c *gin.Context) {
	var request struct {
		Version string `json:"version"`
	}
	if c.ShouldBindJSON(&request) != nil {
		c.JSON(400, gin.H{"error": "invalid_version"})
		return
	}
	status, err := s.updates.Start(c.Request.Context(), request.Version)
	if err != nil {
		c.JSON(409, gin.H{"error": err.Error(), "status": status})
		return
	}
	c.JSON(202, status)
}

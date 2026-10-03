package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/WoAiXueXiHa/LearnQ/internal/domain"
	"github.com/gin-gonic/gin"
)

func (s *Server) registerModelPrice(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4000)
	var row domain.ModelPrice
	if c.ShouldBindJSON(&row) != nil || strings.TrimSpace(row.Model) == "" || len(row.Model) > 128 || strings.TrimSpace(row.VersionLabel) == "" || len(row.VersionLabel) > 255 || row.InputMicroCNYPerMillion < 0 || row.OutputMicroCNYPerMillion < 0 || row.InputMicroCNYPerMillion > 1000000000 || row.OutputMicroCNYPerMillion > 1000000000 || row.MaxInputTokens < 1 || row.MaxInputTokens > 2000000 || row.MaxOutputTokens < 1 || row.MaxOutputTokens > 32768 {
		fail(c, 422, "VALIDATION_FAILED", "invalid model price or token limits", nil)
		return
	}
	row.ID = 0
	row.CreatedAt = time.Now().UTC()
	if err := s.store.DB.WithContext(c.Request.Context()).Create(&row).Error; err != nil {
		fail(c, 500, "INTERNAL_ERROR", "could not preserve model price version", nil)
		return
	}
	ok(c, 201, row)
}

func (s *Server) listModelPrices(c *gin.Context) {
	rows := []domain.ModelPrice{}
	if err := s.store.DB.WithContext(c.Request.Context()).Order("id DESC").Limit(100).Find(&rows).Error; err != nil {
		fail(c, 500, "INTERNAL_ERROR", "could not load model prices", nil)
		return
	}
	ok(c, 200, rows)
}

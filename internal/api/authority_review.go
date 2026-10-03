package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/WoAiXueXiHa/LearnQ/internal/domain"
	"github.com/WoAiXueXiHa/LearnQ/internal/store"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Server) reviewAuthorityCheck(c *gin.Context) {
	id, valid := parseID(c)
	if !valid {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 18000)
	var input struct {
		Disposition string `json:"disposition"`
		Comment     string `json:"comment"`
	}
	if c.ShouldBindJSON(&input) != nil || (input.Disposition != "confirmed" && input.Disposition != "disputed" && input.Disposition != "uncertain") || strings.TrimSpace(input.Comment) == "" || len(input.Comment) > 16000 {
		fail(c, 422, "VALIDATION_FAILED", "invalid check review", nil)
		return
	}
	var row domain.AuthorityCheckReview
	err := s.store.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var check domain.AuthorityCheck
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND status='ready'", id).First(&check).Error; err != nil {
			return err
		}
		var snapshot domain.AuthoritySnapshot
		if err := tx.First(&snapshot, check.AuthoritySnapshotID).Error; err != nil {
			return err
		}
		if input.Disposition == "confirmed" && (check.Judgment == "uncertain" || store.ValidateAuthorityEvidence(snapshot, check.Excerpt) != nil) {
			return gorm.ErrInvalidData
		}
		row = domain.AuthorityCheckReview{AuthorityCheckID: id, Disposition: input.Disposition, Comment: input.Comment, CreatedAt: time.Now().UTC()}
		return tx.Create(&row).Error
	})
	if err != nil {
		fail(c, 409, "CHECK_REVIEW_REJECTED", "uncertain, expired or unavailable evidence cannot be confirmed", nil)
		return
	}
	ok(c, 201, row)
}

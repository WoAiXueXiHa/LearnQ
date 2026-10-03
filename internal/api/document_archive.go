package api

import (
	"time"

	"github.com/WoAiXueXiHa/LearnQ/internal/domain"
	"github.com/gin-gonic/gin"
)

func (s *Server) archiveDocument(c *gin.Context) { s.setDocumentArchived(c, true) }
func (s *Server) restoreDocument(c *gin.Context) { s.setDocumentArchived(c, false) }
func (s *Server) setDocumentArchived(c *gin.Context, archive bool) {
	id, valid := parseID(c)
	if !valid {
		return
	}
	from, to := "ready", "archived"
	if !archive {
		from, to = to, from
	}
	result := s.store.DB.WithContext(c.Request.Context()).Model(&domain.Document{}).Where("id=? AND status=?", id, from).Updates(map[string]any{"status": to, "updated_at": time.Now().UTC()})
	if result.Error != nil {
		fail(c, 500, "INTERNAL_ERROR", "could not update article archive status", nil)
		return
	}
	if result.RowsAffected != 1 {
		fail(c, 409, "DOCUMENT_STATE_CONFLICT", "only ready articles can be archived and only archived articles restored", nil)
		return
	}
	ok(c, 200, gin.H{"document_id": id, "status": to, "history_preserved": true})
}

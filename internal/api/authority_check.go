package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/WoAiXueXiHa/LearnQ/internal/domain"
	"github.com/WoAiXueXiHa/LearnQ/internal/store"
	"github.com/gin-gonic/gin"
)

func (s *Server) listAuthorityClaims(c *gin.Context) {
	id, valid := parseID(c)
	if !valid {
		return
	}
	rows := []domain.AuthorityClaim{}
	if err := s.store.DB.WithContext(c.Request.Context()).Where("answer_feedback_id=?", id).Order("item_ordinal").Find(&rows).Error; err != nil {
		fail(c, 500, "INTERNAL_ERROR", "could not load assertions", nil)
		return
	}
	ok(c, 200, rows)
}

func (s *Server) queueAuthorityCheck(c *gin.Context) {
	id, valid := parseID(c)
	if !valid {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 30000)
	var input struct {
		SnapshotID  uint64 `json:"snapshot_id"`
		Excerpt     string `json:"excerpt"`
		ContextNote string `json:"context_note"`
	}
	if c.ShouldBindJSON(&input) != nil || input.SnapshotID == 0 {
		fail(c, 422, "VALIDATION_FAILED", "invalid authority check evidence", nil)
		return
	}
	check, err := s.store.QueueAuthorityCheck(c.Request.Context(), id, input.SnapshotID, input.Excerpt, input.ContextNote)
	if err != nil {
		fail(c, 409, "AUTHORITY_EVIDENCE_REJECTED", err.Error(), nil)
		return
	}
	ok(c, 202, check)
}

func (s *Server) getAuthorityClaim(c *gin.Context) {
	id, valid := parseID(c)
	if !valid {
		return
	}
	var claim domain.AuthorityClaim
	if lookupFailed(c, s.store.DB.WithContext(c.Request.Context()).First(&claim, id).Error, "could not load assertion") {
		return
	}
	checks := []domain.AuthorityCheck{}
	if err := s.store.DB.WithContext(c.Request.Context()).Where("authority_claim_id=?", id).Order("id").Find(&checks).Error; err != nil {
		fail(c, 500, "INTERNAL_ERROR", "could not load checks", nil)
		return
	}
	results := []gin.H{}
	for _, check := range checks {
		var snapshot domain.AuthoritySnapshot
		if err := s.store.DB.WithContext(c.Request.Context()).First(&snapshot, check.AuthoritySnapshotID).Error; err != nil {
			fail(c, 500, "INTERNAL_ERROR", "could not load check source", nil)
			return
		}
		reviews := []domain.AuthorityCheckReview{}
		if err := s.store.DB.WithContext(c.Request.Context()).Where("authority_check_id=?", check.ID).Order("id").Find(&reviews).Error; err != nil {
			fail(c, 500, "INTERNAL_ERROR", "could not load check reviews", nil)
			return
		}
		results = append(results, gin.H{"check": check, "snapshot": snapshot, "reviews": reviews, "evidence_valid": store.ValidateAuthorityEvidence(snapshot, check.Excerpt) == nil, "expired": !snapshot.ExpiresAt.After(time.Now().UTC())})
	}
	var feedback domain.AnswerFeedback
	if lookupFailed(c, s.store.DB.WithContext(c.Request.Context()).First(&feedback, claim.AnswerFeedbackID).Error, "could not load original feedback") {
		return
	}
	var attempt domain.PracticeAttempt
	if lookupFailed(c, s.store.DB.WithContext(c.Request.Context()).First(&attempt, feedback.PracticeAttemptID).Error, "could not load submitted practice") {
		return
	}
	var set domain.QuestionSet
	if lookupFailed(c, s.store.DB.WithContext(c.Request.Context()).First(&set, attempt.QuestionSetID).Error, "could not load article identity") {
		return
	}
	var question domain.PracticeQuestion
	if lookupFailed(c, s.store.DB.WithContext(c.Request.Context()).Where("question_set_id=? AND ordinal=?", set.ID, feedback.Ordinal).First(&question).Error, "could not load original question") {
		return
	}
	var evidence struct {
		ChunkIDs    []string `json:"chunk_ids"`
		ImageRefIDs []uint64 `json:"image_ref_ids"`
	}
	if json.Unmarshal([]byte(question.EvidenceJSON), &evidence) != nil {
		fail(c, 500, "INTERNAL_ERROR", "invalid question evidence", nil)
		return
	}
	chunks := []domain.DocumentChunk{}
	if len(evidence.ChunkIDs) > 0 {
		if err := s.store.DB.WithContext(c.Request.Context()).Where("id IN ? AND index_id=?", evidence.ChunkIDs, set.IndexID).Find(&chunks).Error; err != nil {
			fail(c, 500, "INTERNAL_ERROR", "could not load article evidence", nil)
			return
		}
	}
	ok(c, 200, gin.H{"claim": claim, "checks": results, "document_id": set.DocumentID, "index_id": set.IndexID, "article_evidence": chunks, "image_ref_ids": evidence.ImageRefIDs})
}

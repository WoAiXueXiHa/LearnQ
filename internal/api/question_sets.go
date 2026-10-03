package api

import (
	"encoding/json"
	"net/http"

	"github.com/WoAiXueXiHa/LearnQ/internal/domain"
	"github.com/gin-gonic/gin"
)

func (s *Server) generateQuestionSet(c *gin.Context) {
	id, valid := parseID(c)
	if !valid {
		return
	}
	set, err := s.store.QueueQuestionSet(c.Request.Context(), id)
	if err != nil {
		fail(c, 409, "ARTICLE_NOT_READY", err.Error(), nil)
		return
	}
	ok(c, 202, set)
}

func (s *Server) listQuestionSets(c *gin.Context) {
	rows := []domain.QuestionSet{}
	if err := s.store.DB.WithContext(c.Request.Context()).Order("id DESC").Limit(100).Find(&rows).Error; err != nil {
		fail(c, 500, "INTERNAL_ERROR", "could not load question sets", nil)
		return
	}
	ok(c, 200, rows)
}

func (s *Server) getQuestionSet(c *gin.Context) {
	id, valid := parseID(c)
	if !valid {
		return
	}
	var set domain.QuestionSet
	if lookupFailed(c, s.store.DB.WithContext(c.Request.Context()).First(&set, id).Error, "could not load question set") {
		return
	}
	questions := []domain.PracticeQuestion{}
	if err := s.store.DB.WithContext(c.Request.Context()).Where("question_set_id=?", id).Order("ordinal").Find(&questions).Error; err != nil {
		fail(c, 500, "INTERNAL_ERROR", "could not load questions", nil)
		return
	}
	// Reference points remain private even during authoring, as required by the
	// all-five submission contract. Editing a prompt retains its private points.
	if set.Status != "draft" {
		ok(c, 200, gin.H{"question_set": set, "questions": questions})
		return
	}
	drafts := []domain.QuestionDraft{}
	for _, q := range questions {
		draft := domain.QuestionDraft{Prompt: q.Prompt}
		var evidence struct {
			ChunkIDs    []string `json:"chunk_ids"`
			ImageRefIDs []uint64 `json:"image_ref_ids"`
		}
		if json.Unmarshal([]byte(q.KnowledgePointsJSON), &draft.KnowledgePoints) != nil || json.Unmarshal([]byte(q.EvidenceJSON), &evidence) != nil {
			fail(c, 500, "INTERNAL_ERROR", "invalid stored question metadata", nil)
			return
		}
		draft.ChunkIDs = evidence.ChunkIDs
		draft.ImageRefIDs = evidence.ImageRefIDs
		drafts = append(drafts, draft)
	}
	ok(c, 200, gin.H{"question_set": set, "questions": drafts})
}

func (s *Server) editQuestionSet(c *gin.Context)    { s.writeQuestionSet(c, false) }
func (s *Server) confirmQuestionSet(c *gin.Context) { s.writeQuestionSet(c, true) }
func (s *Server) writeQuestionSet(c *gin.Context, confirm bool) {
	id, valid := parseID(c)
	if !valid {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 256000)
	var input struct {
		Questions []domain.QuestionDraft `json:"questions"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		fail(c, 422, "VALIDATION_FAILED", "invalid question drafts", nil)
		return
	}
	set, err := s.store.EditQuestionSet(c.Request.Context(), id, input.Questions, confirm)
	if err != nil {
		fail(c, 409, "QUESTION_SET_CONFLICT", err.Error(), nil)
		return
	}
	ok(c, 200, set)
}

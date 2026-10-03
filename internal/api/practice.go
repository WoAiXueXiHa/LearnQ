package api

import (
	"encoding/json"
	"net/http"

	"github.com/WoAiXueXiHa/LearnQ/internal/domain"
	"github.com/gin-gonic/gin"
)

func (s *Server) listPracticeAttempts(c *gin.Context) {
	rows := []domain.PracticeAttempt{}
	if err := s.store.DB.WithContext(c.Request.Context()).Order("id DESC").Limit(100).Find(&rows).Error; err != nil {
		fail(c, 500, "INTERNAL_ERROR", "could not load practice history", nil)
		return
	}
	ok(c, 200, rows)
}

func (s *Server) startPractice(c *gin.Context) {
	id, valid := parseID(c)
	if !valid {
		return
	}
	attempt, err := s.store.StartPractice(c.Request.Context(), id)
	if err != nil {
		fail(c, 409, "PRACTICE_NOT_READY", err.Error(), nil)
		return
	}
	ok(c, 201, attempt)
}

func (s *Server) getPractice(c *gin.Context) {
	id, valid := parseID(c)
	if !valid {
		return
	}
	var attempt domain.PracticeAttempt
	if lookupFailed(c, s.store.DB.WithContext(c.Request.Context()).First(&attempt, id).Error, "could not load practice") {
		return
	}
	questions := []domain.PracticeQuestion{}
	if err := s.store.DB.WithContext(c.Request.Context()).Where("question_set_id=?", attempt.QuestionSetID).Order("ordinal").Find(&questions).Error; err != nil {
		fail(c, 500, "INTERNAL_ERROR", "could not load questions", nil)
		return
	}
	var answers []string
	if err := json.Unmarshal([]byte(attempt.AnswersJSON), &answers); err != nil {
		fail(c, 500, "INTERNAL_ERROR", "invalid stored answers", nil)
		return
	}
	var set domain.QuestionSet
	if lookupFailed(c, s.store.DB.WithContext(c.Request.Context()).First(&set, attempt.QuestionSetID).Error, "could not load practice evidence identity") {
		return
	}
	if attempt.SubmittedAt != nil {
		var feedback []domain.AnswerFeedback
		if err := s.store.DB.WithContext(c.Request.Context()).Where("practice_attempt_id=?", attempt.ID).Order("ordinal").Find(&feedback).Error; err != nil {
			fail(c, 500, "INTERNAL_ERROR", "could not load feedback", nil)
			return
		}
		results := []gin.H{}
		for _, row := range feedback {
			var items []domain.FeedbackItem
			if err := json.Unmarshal([]byte(row.ItemsJSON), &items); err != nil {
				fail(c, 500, "INTERNAL_ERROR", "invalid stored feedback", nil)
				return
			}
			corrections := []domain.FeedbackCorrection{}
			if err := s.store.DB.WithContext(c.Request.Context()).Where("answer_feedback_id=?", row.ID).Order("id").Find(&corrections).Error; err != nil {
				fail(c, 500, "INTERNAL_ERROR", "could not load feedback correction history", nil)
				return
			}
			results = append(results, gin.H{"feedback": row, "items": items, "corrections": corrections})
		}
		metadata, err := s.practiceEvidenceMetadata(c.Request.Context(), set, questions)
		if err != nil {
			fail(c, 500, "INTERNAL_ERROR", "could not load fixed evidence metadata", nil)
			return
		}
		references := []json.RawMessage{}
		referenceItems := []json.RawMessage{}
		referenceEvidence := []json.RawMessage{}
		for _, question := range questions {
			references = append(references, json.RawMessage(question.ReferencePointsJSON))
			items := question.ReferenceItemsJSON
			if items == "" {
				items = "null"
			}
			referenceItems = append(referenceItems, json.RawMessage(items))
			referenceEvidence = append(referenceEvidence, json.RawMessage(question.EvidenceJSON))
		}
		ok(c, 200, gin.H{"attempt": attempt, "questions": questions, "answers": answers, "feedback": results, "reference_points": references, "reference_items": referenceItems, "reference_evidence": referenceEvidence, "reference_metadata": metadata, "document_id": set.DocumentID, "index_id": set.IndexID})
		return
	}
	// PracticeQuestion's JSON contract excludes reference points and evidence.
	ok(c, 200, gin.H{"attempt": attempt, "questions": questions, "answers": answers, "document_id": set.DocumentID, "index_id": set.IndexID})
}

func (s *Server) savePractice(c *gin.Context)   { s.writePractice(c, false) }
func (s *Server) submitPractice(c *gin.Context) { s.writePractice(c, true) }
func (s *Server) writePractice(c *gin.Context, submit bool) {
	id, valid := parseID(c)
	if !valid {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 200000)
	var input struct {
		Answers []string `json:"answers"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		fail(c, 422, "VALIDATION_FAILED", "invalid practice answers", nil)
		return
	}
	attempt, err := s.store.SavePracticeAnswers(c.Request.Context(), id, input.Answers, submit)
	if err != nil {
		fail(c, 409, "PRACTICE_STATE_CONFLICT", err.Error(), nil)
		return
	}
	ok(c, 200, attempt)
}

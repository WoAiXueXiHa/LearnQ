package api

import (
	"net/http"
	"time"

	"github.com/WoAiXueXiHa/LearnQ/internal/domain"
	"github.com/WoAiXueXiHa/LearnQ/internal/store"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Server) correctFeedback(c *gin.Context) {
	id, valid := parseID(c)
	if !valid {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 20000)
	var input struct {
		Disposition string `json:"disposition"`
		Comment     string `json:"comment"`
	}
	if c.ShouldBindJSON(&input) != nil {
		fail(c, 422, "VALIDATION_FAILED", "invalid correction", nil)
		return
	}
	correction, err := s.store.CorrectFeedback(c.Request.Context(), id, input.Disposition, input.Comment)
	if err != nil {
		fail(c, 409, "CORRECTION_REJECTED", err.Error(), nil)
		return
	}
	ok(c, 201, correction)
}

func (s *Server) practiceCorrections(c *gin.Context) {
	id, valid := parseID(c)
	if !valid {
		return
	}
	rows := []domain.FeedbackCorrection{}
	if err := s.store.DB.WithContext(c.Request.Context()).Where("answer_feedback_id=?", id).Order("id").Find(&rows).Error; err != nil {
		fail(c, 500, "INTERNAL_ERROR", "could not load corrections", nil)
		return
	}
	ok(c, 200, rows)
}

func (s *Server) schedulePracticeReview(c *gin.Context) {
	id, valid := parseID(c)
	if !valid {
		return
	}
	var input struct {
		DueAt time.Time `json:"due_at"`
	}
	if c.ShouldBindJSON(&input) != nil || input.DueAt.IsZero() || input.DueAt.Before(time.Now().UTC()) {
		fail(c, 422, "VALIDATION_FAILED", "review due time must be in the future", nil)
		return
	}
	var review domain.PracticeReview
	err := s.store.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var attempt domain.PracticeAttempt
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND status='feedback_ready'", id).First(&attempt).Error; err != nil {
			return err
		}
		now := time.Now().UTC()
		if err := (&store.Store{DB: tx}).ValidatePracticeReview(c.Request.Context(), id); err != nil {
			return err
		}
		review = domain.PracticeReview{PracticeAttemptID: id, QuestionSetID: attempt.QuestionSetID, Status: "scheduled", DueAt: input.DueAt.UTC(), CreatedAt: now, UpdatedAt: now}
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&review).Error
	})
	if err != nil {
		fail(c, 409, "REVIEW_NOT_READY", err.Error(), nil)
		return
	}
	if err := s.store.DB.WithContext(c.Request.Context()).Where("practice_attempt_id=?", id).First(&review).Error; err != nil {
		fail(c, 500, "INTERNAL_ERROR", "could not load review", nil)
		return
	}
	ok(c, 200, review)
}

func (s *Server) listPracticeReviews(c *gin.Context) {
	rows := []domain.PracticeReview{}
	if err := s.store.DB.WithContext(c.Request.Context()).Order("due_at DESC,id DESC").Limit(100).Find(&rows).Error; err != nil {
		fail(c, 500, "INTERNAL_ERROR", "could not load practice reviews", nil)
		return
	}
	ok(c, 200, rows)
}

func (s *Server) startPracticeReview(c *gin.Context) {
	id, valid := parseID(c)
	if !valid {
		return
	}
	var attempt domain.PracticeAttempt
	err := s.store.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var review domain.PracticeReview
		if err := tx.First(&review, id).Error; err != nil {
			return err
		}
		var set domain.QuestionSet
		if err := tx.First(&set, review.QuestionSetID).Error; err != nil {
			return err
		}
		var document domain.Document
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND status IN ('ready','archived')", set.DocumentID).First(&document).Error; err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&review, id).Error; err != nil {
			return err
		}
		if review.CompletedAttemptID != nil {
			return tx.First(&attempt, *review.CompletedAttemptID).Error
		}
		if review.Status != "scheduled" {
			return gorm.ErrRecordNotFound
		}
		if err := (&store.Store{DB: tx}).ValidatePracticeReview(c.Request.Context(), review.PracticeAttemptID); err != nil {
			return err
		}
		var err error
		attempt, err = (&store.Store{DB: tx}).StartPractice(c.Request.Context(), review.QuestionSetID)
		if err != nil {
			return err
		}
		return tx.Model(&review).Updates(map[string]any{"status": "in_progress", "completed_attempt_id": attempt.ID, "updated_at": time.Now().UTC()}).Error
	})
	if err != nil {
		fail(c, 409, "REVIEW_NOT_READY", err.Error(), nil)
		return
	}
	ok(c, 201, attempt)
}

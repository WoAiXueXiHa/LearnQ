package domain

import "time"

type FeedbackCorrection struct {
	ID                uint64    `json:"id"`
	AnswerFeedbackID  uint64    `json:"answer_feedback_id"`
	OriginalItemsJSON string    `json:"-" gorm:"column:original_items_json"`
	Disposition       string    `json:"disposition"`
	Comment           string    `json:"comment"`
	CreatedAt         time.Time `json:"created_at"`
}

type PracticeReview struct {
	ID                 uint64    `json:"id"`
	PracticeAttemptID  uint64    `json:"practice_attempt_id"`
	QuestionSetID      uint64    `json:"question_set_id"`
	Status             string    `json:"status"`
	DueAt              time.Time `json:"due_at"`
	CompletedAttemptID *uint64   `json:"completed_attempt_id,omitempty"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

package domain

import "time"

type QuestionSet struct {
	GenerationTaskID uint64    `json:"generation_task_id"`
	ID               uint64    `json:"id"`
	DocumentID       uint64    `json:"document_id"`
	IndexID          uint64    `json:"index_id"`
	Status           string    `json:"status"`
	Model            string    `json:"model"`
	PromptVersion    string    `json:"prompt_version"`
	OriginalJSON     string    `json:"-" gorm:"column:original_json"`
	LastError        string    `json:"last_error,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// Private answer fields are never serialized by default. Authoring and submitted
// practice views explicitly choose which fields to return.
type PracticeQuestion struct {
	ID                  uint64 `json:"id"`
	QuestionSetID       uint64 `json:"question_set_id"`
	Ordinal             int    `json:"ordinal"`
	Prompt              string `json:"prompt"`
	KnowledgePointsJSON string `json:"-" gorm:"column:knowledge_points_json"`
	ReferenceItemsJSON  string `json:"-" gorm:"column:reference_items_json;default:null"`
	ReferencePointsJSON string `json:"-" gorm:"column:reference_points_json"`
	EvidenceJSON        string `json:"-" gorm:"column:evidence_json"`
}

type QuestionSetEdit struct {
	ID            uint64
	QuestionSetID uint64
	QuestionsJSON string `gorm:"column:questions_json"`
	CreatedAt     time.Time
}

type PracticeAttempt struct {
	ID            uint64     `json:"id"`
	QuestionSetID uint64     `json:"question_set_id"`
	Status        string     `json:"status"`
	AnswersJSON   string     `json:"-" gorm:"column:answers_json"`
	SubmittedAt   *time.Time `json:"submitted_at,omitempty"`
	LastError     string     `json:"last_error,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

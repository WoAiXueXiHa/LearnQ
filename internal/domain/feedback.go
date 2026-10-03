package domain

import "time"

type FeedbackItem struct {
	Type           string   `json:"type"`
	JudgmentStatus string   `json:"judgment_status"`
	Explanation    string   `json:"explanation"`
	ChunkIDs       []string `json:"chunk_ids"`
	ImageRefIDs    []uint64 `json:"image_ref_ids"`
}

type AnswerFeedback struct {
	ID                uint64    `json:"id"`
	PracticeAttemptID uint64    `json:"practice_attempt_id"`
	Ordinal           int       `json:"ordinal"`
	TaskID            uint64    `json:"task_id"`
	Status            string    `json:"status"`
	ItemsJSON         string    `json:"-" gorm:"column:items_json"`
	OriginalJSON      string    `json:"-" gorm:"column:original_json"`
	Model             string    `json:"model"`
	PromptVersion     string    `json:"prompt_version"`
	LastError         string    `json:"last_error,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

func (AnswerFeedback) TableName() string { return "answer_feedback" }

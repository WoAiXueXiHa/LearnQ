package domain

import (
	"fmt"
	"time"
)

func ReviewInterval(mastery int) (time.Duration, error) {
	days := []int{1, 2, 4, 7, 14, 30}
	if mastery < 0 || mastery >= len(days) {
		return 0, fmt.Errorf("mastery must be between 0 and %d", len(days)-1)
	}
	return time.Duration(days[mastery]) * 24 * time.Hour, nil
}

type ReviewTask struct {
	ID          uint64     `json:"id" gorm:"primaryKey"`
	ReportID    uint64     `json:"report_id"`
	Mastery     int        `json:"mastery"`
	Status      string     `json:"status"`
	DueAt       time.Time  `json:"due_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

type ReviewEvent struct {
	ID           uint64 `gorm:"primaryKey"`
	ReviewTaskID uint64
	Action       string
	OldMastery   int
	NewMastery   int
	OldDueAt     time.Time
	NewDueAt     time.Time
	CreatedAt    time.Time
}

type Report struct {
	ID              uint64    `json:"id" gorm:"primaryKey"`
	TaskID          uint64    `json:"task_id"`
	MarkdownContent string    `json:"markdown_content" gorm:"type:longtext"`
	ExportStatus    string    `json:"export_status"`
	ExportError     string    `json:"export_error,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}

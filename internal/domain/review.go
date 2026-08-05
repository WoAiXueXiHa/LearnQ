package domain

import (
	"fmt"
	"time"
)

// ReviewInterval 把掌握度映射为复习间隔天数（1/2/4/7/14/30 天）；越界值返回错误。
func ReviewInterval(mastery int) (time.Duration, error) {
	// 掌握度由用户提交，系统只把它映射为复习间隔，不替用户判断是否已经掌握。
	days := []int{1, 2, 4, 7, 14, 30}
	if mastery < 0 || mastery >= len(days) {
		return 0, fmt.Errorf("mastery must be between 0 and %d", len(days)-1)
	}
	return time.Duration(days[mastery]) * 24 * time.Hour, nil
}

// ReviewTask 是一条间隔复习计划：报告成功后生成，按 DueAt 到期提醒，掌握度随复习更新。
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
	// ReviewTask 保存当前计划，ReviewEvent 追加记录每次完成/跳过前后的值，便于复盘变更历史。
	ID           uint64 `gorm:"primaryKey"`
	ReviewTaskID uint64
	Action       string
	OldMastery   int
	NewMastery   int
	OldDueAt     time.Time
	NewDueAt     time.Time
	CreatedAt    time.Time
}

// Report 是报告产出的持久化结果：Markdown 正文与导出状态分离，导出失败仅记录状态、不影响任务结果。
type Report struct {
	ID              uint64    `json:"id" gorm:"primaryKey"`
	TaskID          uint64    `json:"task_id"`
	MarkdownContent string    `json:"markdown_content" gorm:"type:longtext"`
	ExportStatus    string    `json:"export_status"`
	ExportError     string    `json:"export_error,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}

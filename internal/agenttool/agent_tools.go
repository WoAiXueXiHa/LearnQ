package agenttool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/WoAiXueXiHa/LearnQ/internal/domain"
)

// StudyHistorySearch queries learning facts; the model only consumes results and never builds SQL.
func (s Service) StudyHistorySearch(ctx context.Context, input json.RawMessage) (json.RawMessage, json.RawMessage, error) {
	var request struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(input, &request); err != nil {
		return nil, nil, fmt.Errorf("study history input is invalid: %w", err)
	}
	request.Query = strings.TrimSpace(request.Query)
	if request.Query == "" {
		return nil, nil, fmt.Errorf("study history query is required")
	}
	if request.Limit <= 0 || request.Limit > 10 {
		request.Limit = 5
	}
	type historyRow struct {
		ID       uint64    `json:"id"`
		Title    string    `json:"title"`
		Summary  string    `json:"summary"`
		Duration int       `json:"duration_minutes"`
		Created  time.Time `json:"created_at"`
		Content  string    `json:"content"`
	}
	var rows []historyRow
	pattern := "%" + request.Query + "%"
	err := s.DB.WithContext(ctx).Raw(`SELECT sr.id,sr.title,sr.summary,sr.duration_minute,sr.created_at,
		COALESCE(GROUP_CONCAT(sm.content SEPARATOR ', '),'') content
		FROM study_records sr LEFT JOIN study_modules sm ON sm.study_record_id=sr.id
		WHERE sr.title LIKE ? OR sr.summary LIKE ? OR sm.content LIKE ?
		GROUP BY sr.id ORDER BY sr.created_at DESC LIMIT ?`, pattern, pattern, pattern, request.Limit).Scan(&rows).Error
	if err != nil {
		return nil, nil, err
	}
	body, _ := json.Marshal(map[string]any{"query": request.Query, "records": rows, "count": len(rows), "fact_source": "mysql"})
	return body, json.RawMessage("[]"), nil
}

// ReviewTaskCreate is the only write-capable Agent tool in v1 and requires explicit Runtime permission.
func (s Service) ReviewTaskCreate(ctx context.Context, input json.RawMessage) (json.RawMessage, json.RawMessage, error) {
	var request struct {
		ReportID uint64 `json:"report_id"`
	}
	if err := json.Unmarshal(input, &request); err != nil || request.ReportID == 0 {
		return nil, nil, fmt.Errorf("report_id is required")
	}
	var report domain.Report
	if err := s.DB.WithContext(ctx).First(&report, request.ReportID).Error; err != nil {
		return nil, nil, fmt.Errorf("report %d is not available: %w", request.ReportID, err)
	}
	var existing domain.ReviewTask
	if err := s.DB.WithContext(ctx).Where("report_id=? AND status='scheduled'", request.ReportID).Order("id").First(&existing).Error; err == nil {
		body, _ := json.Marshal(map[string]any{"status": "exists", "review_task": existing})
		return body, json.RawMessage("[]"), nil
	}
	now := time.Now().UTC()
	task := domain.ReviewTask{ReportID: request.ReportID, Mastery: 0, Status: "scheduled", DueAt: now.Add(24 * time.Hour), CreatedAt: now, UpdatedAt: now}
	if err := s.DB.WithContext(ctx).Create(&task).Error; err != nil {
		return nil, nil, err
	}
	body, _ := json.Marshal(map[string]any{"status": "created", "review_task": task})
	return body, json.RawMessage("[]"), nil
}

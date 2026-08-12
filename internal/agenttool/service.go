package agenttool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/WoAiXueXiHa/LearnQ/internal/rag"
	"gorm.io/gorm"
)

type Service struct {
	DB       *gorm.DB
	Evidence *rag.EvidenceService
}

func (s Service) WeeklyStats(ctx context.Context, _ json.RawMessage) (json.RawMessage, json.RawMessage, error) {
	// 统计事实由 SQL 计算，模型只负责解释，避免让 LLM 自己从自然语言估算时长和次数。
	// 近 7 天滚动窗口（UTC），统计口径与页面展示一致。
	since := time.Now().UTC().AddDate(0, 0, -7)
	var summary struct {
		Minutes int `json:"minutes"`
		Records int `json:"records"`
	}
	var categories []struct {
		Category string `json:"category"`
		Count    int    `json:"count"`
	}
	// COALESCE 保证近 7 天无记录时 minutes 仍为 0 而非 NULL，JSON 输出字段始终存在。
	if err := s.DB.WithContext(ctx).Raw(`SELECT COALESCE(SUM(duration_minute),0) minutes,
		COUNT(*) records FROM study_records WHERE created_at>=?`, since).Scan(&summary).Error; err != nil {
		return nil, nil, err
	}
	if err := s.DB.WithContext(ctx).Raw(`SELECT sm.category,COUNT(*) count FROM study_modules sm
		JOIN study_records sr ON sr.id=sm.study_record_id WHERE sr.created_at>=?
		GROUP BY sm.category ORDER BY count DESC,sm.category`, since).Scan(&categories).Error; err != nil {
		return nil, nil, err
	}
	body, _ := json.Marshal(map[string]any{
		"period_days": 7, "minutes": summary.Minutes, "records": summary.Records,
		"module_categories": categories, "fact_source": "mysql",
	})
	return body, json.RawMessage("[]"), nil
}

func (s Service) RAGQuery(ctx context.Context, input json.RawMessage) (json.RawMessage, json.RawMessage, error) {
	question := questionFrom(input)
	if question == "" {
		return nil, nil, fmt.Errorf("rag_query needs question, topic, summary or title")
	}
	evidence, err := s.Evidence.Search(ctx, question, 5)
	if err != nil {
		return nil, nil, err
	}
	if len(evidence) == 0 {
		// 无可用证据时给出空答案并标记 no_evidence，避免模型基于幻觉作答。
		body, _ := json.Marshal(map[string]any{"question": question, "answer": "", "status": "no_evidence"})
		return body, json.RawMessage("[]"), nil
	}
	body, _ := json.Marshal(map[string]any{
		"question": question, "status": "grounded", "evidence": evidence,
	})
	citationBody, _ := json.Marshal(evidence)
	return body, citationBody, nil
}

// questionFrom 按固定优先级从输入提取检索问题（question > topic > summary > title），
// 兼容不同 Skill 传入的字段命名；全部缺失或为空时返回空串，由调用方报错。
func questionFrom(raw json.RawMessage) string {
	var input map[string]any
	_ = json.Unmarshal(raw, &input)
	for _, key := range []string{"question", "topic", "summary", "title"} {
		if value, ok := input[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// truncate 按 rune 截断并在尾部追加省略号，避免切断 UTF-8 多字节字符。

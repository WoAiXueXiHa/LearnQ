package api

import (
	"github.com/gin-gonic/gin"
	"net/http"
	"strings"
	"time"
)

func WithModelBudget(daily, reserve int64) Option {
	return func(s *Server) { s.dailyBudgetMicroCNY = daily; s.callReserveMicroCNY = reserve }
}

func (s *Server) modelBudget(c *gin.Context) {
	day := time.Now().UTC().Format("2006-01-02")
	var reserved int64
	if err := s.store.DB.WithContext(c.Request.Context()).Table("model_daily_budgets").Select("COALESCE(SUM(reserved_microcny),0)").Where("day=?", day).Scan(&reserved).Error; err != nil {
		fail(c, 500, "INTERNAL_ERROR", "could not load model budget", nil)
		return
	}
	ok(c, 200, gin.H{"mode": s.aiMode, "day": day, "day_timezone": "UTC", "daily_budget_microcny": s.dailyBudgetMicroCNY, "call_reserve_microcny": s.callReserveMicroCNY, "reserved_microcny": reserved, "reservation_is_billing": false})
}

func (s *Server) modelCalls(c *gin.Context) {
	rows := []map[string]any{}
	if err := s.store.DB.WithContext(c.Request.Context()).Table("model_calls").Order("id DESC").Limit(100).Find(&rows).Error; err != nil {
		fail(c, 500, "INTERNAL_ERROR", "could not load model call ledger", nil)
		return
	}
	ids := make([]any, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row["id"])
	}
	billings := []map[string]any{}
	if len(ids) > 0 {
		if err := s.store.DB.WithContext(c.Request.Context()).Table("model_call_billings").Where("model_call_id IN ?", ids).Order("id DESC").Limit(500).Find(&billings).Error; err != nil {
			fail(c, 500, "INTERNAL_ERROR", "could not load billing evidence", nil)
			return
		}
	}
	ok(c, 200, gin.H{"calls": rows, "billings": billings, "reservation_unit": "microcny", "reservation_is_billing": false, "day_timezone": "UTC"})
}

func (s *Server) reconcileModelBilling(c *gin.Context) {
	id, valid := parseID(c)
	if !valid {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 3000)
	var input struct {
		BilledMicroCNY int64  `json:"billed_microcny"`
		Note           string `json:"note"`
	}
	if c.ShouldBindJSON(&input) != nil || input.BilledMicroCNY < 0 || input.BilledMicroCNY > 1000000000000 || strings.TrimSpace(input.Note) == "" || len(input.Note) > 1024 {
		fail(c, 422, "VALIDATION_FAILED", "invalid billing evidence", nil)
		return
	}
	var call struct {
		ID                uint64
		Mode              string
		EstimatedMicroCNY *int64
	}
	if lookupFailed(c, s.store.DB.WithContext(c.Request.Context()).Table("model_calls").Where("id=?", id).First(&call).Error, "could not load model call") {
		return
	}
	if call.Mode != "real" {
		fail(c, 409, "INVALID_STATE", "fake calls cannot have provider billing", nil)
		return
	}
	if err := s.store.DB.WithContext(c.Request.Context()).Exec("INSERT INTO model_call_billings(model_call_id,billed_microcny,note,created_at) VALUES (?,?,?,?)", id, input.BilledMicroCNY, input.Note, time.Now().UTC()).Error; err != nil {
		fail(c, 500, "INTERNAL_ERROR", "could not preserve billing reconciliation", nil)
		return
	}
	var difference *int64
	if call.EstimatedMicroCNY != nil {
		delta := input.BilledMicroCNY - *call.EstimatedMicroCNY
		difference = &delta
	}
	ok(c, 201, gin.H{"model_call_id": id, "billed_microcny": input.BilledMicroCNY, "estimated_microcny": call.EstimatedMicroCNY, "difference_microcny": difference, "evidence": "user_entered_billing_note"})
}

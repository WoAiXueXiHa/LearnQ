package api

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	appmetrics "github.com/WoAiXueXiHa/LearnQ/internal/metrics"
	"github.com/gin-gonic/gin"
)

func metricMiddleware(recorder *appmetrics.Recorder) gin.HandlerFunc {
	return func(c *gin.Context) {
		started := time.Now()
		c.Next()
		if recorder == nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		recorder.Inc(ctx, "http_requests_total")
		if c.Writer.Status() >= 500 {
			recorder.Inc(ctx, "http_errors_total")
		}
		if c.FullPath() == "/api/v1/rag/query" {
			recorder.Observe(ctx, "rag_request_latency", time.Since(started))
			if c.Writer.Status() >= 400 {
				recorder.Inc(ctx, "rag_errors_total")
			}
		}
	}
}

func (s *Server) prometheusMetrics(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()

	type taskCount struct {
		Status string
		Count  int64
	}
	var tasks []taskCount
	if err := s.store.DB.WithContext(ctx).Table("ai_tasks").Select("status, COUNT(*) count").
		Group("status").Scan(&tasks).Error; err != nil {
		c.String(http.StatusServiceUnavailable, "metrics database query failed\n")
		return
	}
	var outbox struct {
		Count         int64
		OldestSeconds *float64
	}
	if err := s.store.DB.WithContext(ctx).Raw(`SELECT COUNT(*) count,
		TIMESTAMPDIFF(MICROSECOND, MIN(created_at), UTC_TIMESTAMP(6))/1000000 oldest_seconds
		FROM outbox_events WHERE published_at IS NULL`).Scan(&outbox).Error; err != nil {
		c.String(http.StatusServiceUnavailable, "metrics outbox query failed\n")
		return
	}
	var expired int64
	if err := s.store.DB.WithContext(ctx).Table("ai_tasks").
		Where("status='processing' AND lease_until<=?", time.Now().UTC()).Count(&expired).Error; err != nil {
		c.String(http.StatusServiceUnavailable, "metrics lease query failed\n")
		return
	}

	var builder strings.Builder
	for _, task := range tasks {
		fmt.Fprintf(&builder, "learnq_tasks{status=%s} %d\n", strconv.Quote(task.Status), task.Count)
	}
	fmt.Fprintf(&builder, "learnq_outbox_unpublished %d\n", outbox.Count)
	oldest := 0.0
	if outbox.OldestSeconds != nil {
		oldest = *outbox.OldestSeconds
	}
	fmt.Fprintf(&builder, "learnq_outbox_oldest_wait_seconds %.6f\n", oldest)
	fmt.Fprintf(&builder, "learnq_expired_leases %d\n", expired)

	if s.queueDepth != nil {
		ready, processing, err := s.queueDepth(ctx)
		if err == nil {
			fmt.Fprintf(&builder, "learnq_queue_ready %d\n", ready)
			fmt.Fprintf(&builder, "learnq_queue_processing %d\n", processing)
		}
	}
	if s.metrics != nil {
		values, err := s.metrics.Snapshot(ctx)
		if err == nil {
			names := make([]string, 0, len(values))
			for name := range values {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				fmt.Fprintf(&builder, "learnq_%s %.0f\n", name, values[name])
			}
		}
	}
	c.Data(http.StatusOK, "text/plain; version=0.0.4; charset=utf-8", []byte(builder.String()))
}

//go:build integration

package trace_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/WoAiXueXiHa/LearnQ/internal/bootstrap"
	"github.com/WoAiXueXiHa/LearnQ/internal/config"
	"github.com/WoAiXueXiHa/LearnQ/internal/migrate"
	"github.com/WoAiXueXiHa/LearnQ/internal/model"
	"github.com/WoAiXueXiHa/LearnQ/internal/skill"
	"github.com/WoAiXueXiHa/LearnQ/internal/trace"
)

func TestRecorderHonorsCanceledContext(t *testing.T) {
	dsn := os.Getenv("LEARNQ_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("LEARNQ_TEST_MYSQL_DSN is required")
	}
	cfg := config.Load()
	cfg.MySQLDSN = dsn
	db, err := bootstrap.MySQL(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate.Run(db); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Exec("DELETE FROM tool_calls")
		db.Exec("DELETE FROM agent_steps")
		db.Exec("DELETE FROM agent_runs")
	})
	definition, _ := skill.New(model.Fake{}).Get("daily-review")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = (trace.Recorder{DB: db}).Record(ctx, 0, "integration", definition, "hash", `{}`,
		model.ChatResponse{Content: `{}`, Model: "fake"}, time.Millisecond, nil, nil)
	if err == nil {
		t.Fatal("canceled trace context was ignored")
	}
	var runs int64
	if err := db.Table("agent_runs").Count(&runs).Error; err != nil {
		t.Fatal(err)
	}
	if runs != 0 {
		t.Fatalf("partial trace persisted: runs=%d", runs)
	}
}

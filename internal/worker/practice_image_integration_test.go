//go:build integration

package worker_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/WoAiXueXiHa/LearnQ/internal/articleimage"
	"github.com/WoAiXueXiHa/LearnQ/internal/bootstrap"
	"github.com/WoAiXueXiHa/LearnQ/internal/config"
	"github.com/WoAiXueXiHa/LearnQ/internal/domain"
	"github.com/WoAiXueXiHa/LearnQ/internal/imageindex"
	"github.com/WoAiXueXiHa/LearnQ/internal/imagestore"
	"github.com/WoAiXueXiHa/LearnQ/internal/indexer"
	"github.com/WoAiXueXiHa/LearnQ/internal/migrate"
	"github.com/WoAiXueXiHa/LearnQ/internal/model"
	"github.com/WoAiXueXiHa/LearnQ/internal/queue"
	"github.com/WoAiXueXiHa/LearnQ/internal/rag"
	"github.com/WoAiXueXiHa/LearnQ/internal/store"
	"github.com/WoAiXueXiHa/LearnQ/internal/worker"
	"github.com/go-redis/redis/v8"
)

type controlledVectorTransport struct{}

func (controlledVectorTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"status":"ok","result":true}`)), Request: req}, nil
}

type observedPracticeModel struct {
	mu                 sync.Mutex
	imageFeedbackCalls int
	fail               bool
}

func (m *observedPracticeModel) Generate(ctx context.Context, req model.ChatRequest) (model.ChatResponse, error) {
	m.mu.Lock()
	fail := m.fail
	m.mu.Unlock()
	if fail && req.Skill == "practice-feedback" && strings.Contains(string(req.Input), "SIMULATE_FAILURE") {
		return model.ChatResponse{}, model.Permanent(errors.New("controlled feedback failure"))
	}
	if req.Skill == "practice-feedback" && strings.Contains(string(req.Input), "image_ref_id") && strings.Contains(string(req.Input), "description") {
		m.mu.Lock()
		m.imageFeedbackCalls++
		m.mu.Unlock()
	}
	return (model.Fake{}).Generate(ctx, req)
}

// The download and vector HTTP boundary are controlled mocks; MySQL, Redis,
// filesystem snapshots, task workers and the product state transitions are real.
func TestSnapshotVisionQuestionsFeedbackWithControlledDownload(t *testing.T) {
	cfg := config.Load()
	cfg.MySQLDSN = os.Getenv("LEARNQ_TEST_MYSQL_DSN")
	addr := os.Getenv("LEARNQ_TEST_REDIS_ADDR")
	if cfg.MySQLDSN == "" || addr == "" {
		t.Skip("dedicated integration dependencies required")
	}
	db, err := bootstrap.MySQL(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate.Run(db); err != nil {
		t.Fatal(err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	ctx := context.Background()
	rdb.FlushDB(ctx)
	defer rdb.Close()
	s := store.New(db)
	q := queue.New(rdb)
	images := imagestore.New(t.TempDir())
	hash := func(text string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(text))) }
	source := "# 图片事务\n\n正文解释整体提交与回滚。\n\n![自有合成图](https://fixture.invalid/owned.png)\n"
	doc, task, err := s.CreateDocument(ctx, domain.Document{Filename: "owned.md", MediaType: "md", Content: source, ContentHash: hash(source)})
	if err != nil {
		t.Fatal(err)
	}
	vectorStore := rag.Qdrant{BaseURL: "http://controlled.invalid", Collection: "controlled", Client: &http.Client{Transport: controlledVectorTransport{}}}
	if err := s.DispatchOutbox(ctx, func(context.Context, uint64, time.Time) error { return nil }, 100); err != nil {
		t.Fatal(err)
	}
	acquired, ok, err := s.Acquire(ctx, task.ID, "text", time.Now().Add(time.Minute))
	if err != nil || !ok {
		t.Fatalf("acquire: %v", err)
	}
	textIndexer := indexer.Indexer{Store: s, Embedding: model.Fake{Dimension: 64}, Vectors: vectorStore, Dimension: 64, ChunkVersion: rag.MarkdownChunkVersion, Version: "embedding=learnq-fake-hash-v1;dim=64"}
	if _, err := textIndexer.Process(ctx, acquired); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteDocumentIndex(ctx, acquired, "text", doc.ID, textIndexer.IndexVersion()); err != nil {
		t.Fatal(err)
	}
	db.First(&doc, doc.ID)
	var ref domain.ArticleImage
	if err := db.Where("index_id=?", doc.ActiveIndexID).First(&ref).Error; err != nil {
		t.Fatal(err)
	}
	var pngBytes bytes.Buffer
	if err := png.Encode(&pngBytes, image.NewRGBA(image.Rect(0, 0, 20, 20))); err != nil {
		t.Fatal(err)
	}
	download := &imagestore.RemoteImage{Body: pngBytes.Bytes(), SHA256: hash(pngBytes.String()), MediaType: "image/png", Extension: ".png", Width: 20, Height: 20}
	processor := articleimage.Processor{Store: s, Images: images, DescriptionModel: "learnq-fake-vision-v1", Fetch: func(context.Context, string) (*imagestore.RemoteImage, error) { return download, nil }}
	snapshot, err := processor.Process(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	var setID, attemptID uint64
	var recoveryAttemptID uint64
	t.Cleanup(func() {
		rdb.FlushDB(context.Background())
		for _, query := range []string{"DELETE FROM answer_feedback WHERE practice_attempt_id=?", "DELETE FROM practice_attempts WHERE id=?"} {
			db.Exec(query, attemptID)
			db.Exec(query, recoveryAttemptID)
		}
		db.Where("question_set_id=?", setID).Delete(&domain.QuestionSetEdit{})
		db.Where("question_set_id=?", setID).Delete(&domain.PracticeQuestion{})
		db.Delete(&domain.QuestionSet{}, setID)
		db.Where("document_id=?", doc.ID).Delete(&domain.ImageEvidence{})
		db.Where("document_id=?", doc.ID).Delete(&domain.ArticleImage{})
		db.Exec("DELETE FROM image_description_cache WHERE image_id=?", snapshot.ID)
		db.Delete(&snapshot)
		db.Where("document_id=?", doc.ID).Delete(&domain.DocumentChunk{})
		db.Where("document_id=?", doc.ID).Delete(&domain.DocumentIndex{})
		db.Delete(&doc)
		for _, table := range []string{"task_attempts", "outbox_events", "ai_tasks"} {
			db.Exec("DELETE FROM " + table)
		}
	})
	observed := &observedPracticeModel{}
	pool := worker.New(s, q, observed, t.TempDir(), slog.New(slog.NewTextHandler(os.Stderr, nil))).WithVision(model.Fake{}, images)
	runCtx, cancel := context.WithCancel(ctx)
	pool.Run(runCtx)
	defer func() { cancel(); pool.Wait() }()
	wait := func(description string, ready func() bool) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			if err := s.DispatchOutbox(ctx, q.Enqueue, 100); err != nil {
				t.Fatal(err)
			}
			if ready() {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		var pending []domain.AITask
		db.Find(&pending)
		for _, task := range pending {
			t.Logf("task %d %s status=%s error=%s", task.ID, task.Kind, task.Status, task.LastError)
		}
		t.Fatal("timed out: " + description)
	}
	wait("vision", func() bool { db.First(&snapshot, snapshot.ID); return snapshot.Status == "ready" })
	imageIndexer := imageindex.Indexer{DB: db, Embedding: model.Fake{Dimension: 64}, Vectors: vectorStore, Model: "learnq-fake-hash-v1", Dimension: 64}
	if err := imageIndexer.Process(ctx, ref.ID); err != nil {
		t.Fatal(err)
	}
	set, err := s.QueueQuestionSet(ctx, doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	setID = set.ID
	wait("question set", func() bool { db.First(&set, set.ID); return set.Status == "draft" })
	var output struct {
		Questions []domain.QuestionDraft `json:"questions"`
	}
	if err := json.Unmarshal([]byte(set.OriginalJSON), &output); err != nil {
		t.Fatal(err)
	}
	if len(output.Questions) != 5 || len(output.Questions[0].ImageRefIDs) != 1 {
		t.Fatal("image source omitted")
	}
	if _, err := s.EditQuestionSet(ctx, set.ID, output.Questions, true); err != nil {
		t.Fatal(err)
	}
	attempt, err := s.StartPractice(ctx, set.ID)
	if err != nil {
		t.Fatal(err)
	}
	attemptID = attempt.ID
	if _, err := s.SavePracticeAnswers(ctx, attempt.ID, []string{"答1", "答2", "答3", "答4", "答5"}, true); err != nil {
		t.Fatal(err)
	}
	wait("feedback", func() bool { db.First(&attempt, attempt.ID); return attempt.Status == "feedback_ready" })
	observed.mu.Lock()
	count := observed.imageFeedbackCalls
	observed.mu.Unlock()
	if count < 1 {
		t.Fatal("image description never reached feedback model")
	}
	recoveryAttempt, err := s.StartPractice(ctx, set.ID)
	if err != nil {
		t.Fatal(err)
	}
	recoveryAttemptID = recoveryAttempt.ID
	observed.mu.Lock()
	observed.fail = true
	observed.mu.Unlock()
	if _, err := s.SavePracticeAnswers(ctx, recoveryAttempt.ID, []string{"SIMULATE_FAILURE", "SIMULATE_FAILURE", "答3", "答4", "答5"}, true); err != nil {
		t.Fatal(err)
	}
	wait("two failures and three ready feedbacks", func() bool {
		var rows []domain.AnswerFeedback
		db.Where("practice_attempt_id=?", recoveryAttempt.ID).Find(&rows)
		failed, ready := 0, 0
		for _, row := range rows {
			if row.Status == "failed" {
				failed++
			}
			if row.Status == "ready" {
				ready++
			}
		}
		return failed == 2 && ready == 3
	})
	observed.mu.Lock()
	observed.fail = false
	observed.mu.Unlock()
	var failedFeedback []domain.AnswerFeedback
	db.Where("practice_attempt_id=? AND status='failed'", recoveryAttempt.ID).Find(&failedFeedback)
	retryErrors := make(chan error, len(failedFeedback))
	var retryWG sync.WaitGroup
	for _, row := range failedFeedback {
		retryWG.Add(1)
		go func(taskID uint64) { defer retryWG.Done(); retryErrors <- s.RetryPracticeTask(ctx, taskID) }(row.TaskID)
	}
	retryWG.Wait()
	close(retryErrors)
	for err := range retryErrors {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := db.First(&recoveryAttempt, recoveryAttempt.ID).Error; err != nil {
		t.Fatal(err)
	}
	if recoveryAttempt.Status != "feedback_pending" {
		t.Fatalf("concurrent retry left status=%s", recoveryAttempt.Status)
	}
	wait("recovered feedback", func() bool {
		db.First(&recoveryAttempt, recoveryAttempt.ID)
		return recoveryAttempt.Status == "feedback_ready"
	})
	body, err := images.Read(snapshot.StoragePath)
	if err != nil || !bytes.Equal(body, download.Body) {
		t.Fatalf("snapshot changed: %v", err)
	}
}

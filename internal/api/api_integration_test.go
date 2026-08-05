//go:build integration

package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/WoAiXueXiHa/LeranQ/internal/api"
	"github.com/WoAiXueXiHa/LeranQ/internal/bootstrap"
	"github.com/WoAiXueXiHa/LeranQ/internal/config"
	"github.com/WoAiXueXiHa/LeranQ/internal/domain"
	"github.com/WoAiXueXiHa/LeranQ/internal/imagestore"
	"github.com/WoAiXueXiHa/LeranQ/internal/migrate"
	"github.com/WoAiXueXiHa/LeranQ/internal/model"
	"github.com/WoAiXueXiHa/LeranQ/internal/skill"
	"github.com/WoAiXueXiHa/LeranQ/internal/store"
)

type fixture struct {
	handler http.Handler
	store   *store.Store
}

func setup(t *testing.T) fixture {
	t.Helper()
	cfg := config.Load()
	cfg.MySQLDSN = os.Getenv("LEARNQ_TEST_MYSQL_DSN")
	if cfg.MySQLDSN == "" {
		t.Skip("LEARNQ_TEST_MYSQL_DSN is required")
	}
	db, err := bootstrap.MySQL(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate.Run(db); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, table := range []string{"rag_evaluations", "document_chunks", "images", "documents", "agent_steps", "tool_calls", "agent_runs", "review_events", "review_tasks", "reports", "task_attempts", "outbox_events", "ai_tasks", "study_modules", "study_records", "idempotency_keys"} {
			db.Exec("DELETE FROM " + table)
		}
	})
	s := store.New(db)
	return fixture{
		handler: api.New(s, skill.New(model.Fake{}), api.WithImageStore(imagestore.New(t.TempDir()))).Handler(),
		store:   s,
	}
}

func request(t *testing.T, f fixture, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	f.handler.ServeHTTP(rec, req)
	if rec.Header().Get("X-Request-ID") == "" {
		t.Fatal("X-Request-ID missing")
	}
	return rec
}

func TestStudyRecordIdempotencyEnvelopeAndTaskQuery(t *testing.T) {
	f := setup(t)
	body := `{"title":"API","summary":"idempotency","duration_minutes":30,"modules":[{"category":"backend","content":"HTTP"}]}`
	headers := map[string]string{"Content-Type": "application/json", "Idempotency-Key": "same-key"}
	first := request(t, f, http.MethodPost, "/api/v1/study-records", body, headers)
	if first.Code != http.StatusAccepted {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
	}
	var envelope struct {
		Data struct {
			TaskID uint64 `json:"task_id"`
		} `json:"data"`
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &envelope); err != nil || envelope.Data.TaskID == 0 || envelope.RequestID == "" {
		t.Fatalf("envelope=%#v err=%v", envelope, err)
	}
	second := request(t, f, http.MethodPost, "/api/v1/study-records", body, headers)
	var firstJSON, secondJSON any
	_ = json.Unmarshal(first.Body.Bytes(), &firstJSON)
	_ = json.Unmarshal(second.Body.Bytes(), &secondJSON)
	if second.Code != first.Code || !reflect.DeepEqual(firstJSON, secondJSON) {
		t.Fatalf("idempotent replay differs\nfirst=%s\nsecond=%s", first.Body, second.Body)
	}
	conflict := request(t, f, http.MethodPost, "/api/v1/study-records", strings.Replace(body, `"API"`, `"different"`, 1), headers)
	if conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), "IDEMPOTENCY_CONFLICT") {
		t.Fatalf("conflict status=%d body=%s", conflict.Code, conflict.Body)
	}
	task := request(t, f, http.MethodGet, "/api/v1/tasks/"+strconvFormat(envelope.Data.TaskID), "", nil)
	if task.Code != http.StatusOK || !strings.Contains(task.Body.String(), `"pending"`) {
		t.Fatalf("task status=%d body=%s", task.Code, task.Body)
	}
	invalid := request(t, f, http.MethodPost, "/api/v1/study-records", `{}`, map[string]string{"Content-Type": "application/json"})
	if invalid.Code != 422 || !strings.Contains(invalid.Body.String(), `"code":"VALIDATION_FAILED"`) {
		t.Fatalf("invalid status=%d body=%s", invalid.Code, invalid.Body)
	}
}

func TestImageUploadListContentAndDelete(t *testing.T) {
	f := setup(t)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "note.png")
	if err != nil {
		t.Fatal(err)
	}
	picture := image.NewRGBA(image.Rect(0, 0, 2, 2))
	picture.Set(0, 0, color.RGBA{R: 20, G: 90, B: 140, A: 255})
	if err := png.Encode(part, picture); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("prompt", "提取学习重点"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/images", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	f.handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("upload=%d %s", recorder.Code, recorder.Body)
	}
	var envelope struct {
		Data struct {
			ID     uint64 `json:"id"`
			TaskID uint64 `json:"task_id"`
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil ||
		envelope.Data.ID == 0 || envelope.Data.TaskID == 0 || envelope.Data.Status != "uploaded" {
		t.Fatalf("upload envelope=%#v err=%v", envelope, err)
	}
	id := strconvFormat(envelope.Data.ID)
	list := request(t, f, http.MethodGet, "/api/v1/images", "", nil)
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"note.png"`) {
		t.Fatalf("list=%d %s", list.Code, list.Body)
	}
	content := request(t, f, http.MethodGet, "/api/v1/images/"+id+"/content", "", nil)
	if content.Code != http.StatusOK || content.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("content=%d type=%s", content.Code, content.Header().Get("Content-Type"))
	}
	deleted := request(t, f, http.MethodDelete, "/api/v1/images/"+id, "", nil)
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete=%d %s", deleted.Code, deleted.Body)
	}
	missing := request(t, f, http.MethodGet, "/api/v1/images/"+id, "", nil)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing=%d %s", missing.Code, missing.Body)
	}
}

func TestStudyRecordRecentDuplicateReuseAndForceCreate(t *testing.T) {
	f := setup(t)
	body := `{"title":"重复检查","summary":"相同内容","duration_minutes":20,"modules":[{"category":"project","content":"outbox"}]}`
	first := request(t, f, http.MethodPost, "/api/v1/study-records", body,
		map[string]string{"Content-Type": "application/json", "Idempotency-Key": "duplicate-1"})
	second := request(t, f, http.MethodPost, "/api/v1/study-records", body,
		map[string]string{"Content-Type": "application/json", "Idempotency-Key": "duplicate-2"})
	if first.Code != 202 || second.Code != 202 || !strings.Contains(second.Body.String(), `"deduplicated":true`) {
		t.Fatalf("first=%d %s second=%d %s", first.Code, first.Body, second.Code, second.Body)
	}
	var records, tasks int64
	f.store.DB.Model(&domain.StudyRecord{}).Count(&records)
	f.store.DB.Model(&domain.AITask{}).Count(&tasks)
	if records != 1 || tasks != 1 {
		t.Fatalf("records=%d tasks=%d", records, tasks)
	}
	forced := strings.Replace(body, `"duration_minutes":20`, `"duration_minutes":20,"force_create":true`, 1)
	third := request(t, f, http.MethodPost, "/api/v1/study-records", forced,
		map[string]string{"Content-Type": "application/json", "Idempotency-Key": "duplicate-3"})
	if third.Code != 202 || strings.Contains(third.Body.String(), `"deduplicated":true`) {
		t.Fatalf("forced=%d %s", third.Code, third.Body)
	}
	f.store.DB.Model(&domain.StudyRecord{}).Count(&records)
	if records != 2 {
		t.Fatalf("forced records=%d", records)
	}
	list := request(t, f, http.MethodGet, "/api/v1/study-records", "", nil)
	if list.Code != 200 || !strings.Contains(list.Body.String(), `"task_status":"pending"`) {
		t.Fatalf("list=%d %s", list.Code, list.Body)
	}
}

func TestTaskDetailAndUpcomingReviewReadModels(t *testing.T) {
	f := setup(t)
	record, task, err := f.store.CreateStudyRecord(context.Background(), store.CreateRecord{
		Title: "聚合详情", Summary: "报告和复习", DurationMinutes: 15,
		Modules: []domain.StudyModule{{Category: "backend", Content: "Go"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	report := domain.Report{TaskID: task.ID, MarkdownContent: "# 报告\n内容", ExportStatus: "succeeded", CreatedAt: now}
	if err := f.store.DB.Create(&report).Error; err != nil {
		t.Fatal(err)
	}
	review := domain.ReviewTask{ReportID: report.ID, Mastery: 0, Status: "scheduled", DueAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now}
	if err := f.store.DB.Create(&review).Error; err != nil {
		t.Fatal(err)
	}
	detail := request(t, f, http.MethodGet, "/api/v1/tasks/"+strconvFormat(task.ID)+"/detail", "", nil)
	if detail.Code != 200 || !strings.Contains(detail.Body.String(), record.Title) || !strings.Contains(detail.Body.String(), `"review_task"`) {
		t.Fatalf("detail=%d %s", detail.Code, detail.Body)
	}
	upcoming := request(t, f, http.MethodGet, "/api/v1/review-tasks?scope=upcoming", "", nil)
	if upcoming.Code != 200 || !strings.Contains(upcoming.Body.String(), "聚合详情") {
		t.Fatalf("upcoming=%d %s", upcoming.Code, upcoming.Body)
	}
}

func TestReviewTaskListsReturnEmptyArrays(t *testing.T) {
	f := setup(t)
	for _, scope := range []string{"due", "upcoming", "all"} {
		response := request(t, f, http.MethodGet, "/api/v1/review-tasks?scope="+scope, "", nil)
		if response.Code != http.StatusOK {
			t.Fatalf("scope=%s status=%d body=%s", scope, response.Code, response.Body)
		}
		if !strings.Contains(response.Body.String(), `"data":[]`) {
			t.Fatalf("scope=%s must return an empty JSON array: %s", scope, response.Body)
		}
	}
}

func TestReviewCompleteAndSkipPersistEvents(t *testing.T) {
	f := setup(t)
	now := time.Now().UTC()
	report := domain.Report{TaskID: 999, MarkdownContent: "# report", ExportStatus: "succeeded", CreatedAt: now}
	if err := f.store.DB.Create(&report).Error; err != nil {
		t.Fatal(err)
	}
	review := domain.ReviewTask{ReportID: report.ID, Mastery: 1, Status: "scheduled", DueAt: now.Add(-time.Hour), CreatedAt: now, UpdatedAt: now}
	if err := f.store.DB.Create(&review).Error; err != nil {
		t.Fatal(err)
	}
	missing := request(t, f, http.MethodPost, "/api/v1/review-tasks/"+strconvFormat(review.ID)+"/complete", `{}`, map[string]string{"Content-Type": "application/json"})
	if missing.Code != 422 || !strings.Contains(missing.Body.String(), "mastery is required") {
		t.Fatalf("missing mastery=%d %s", missing.Code, missing.Body)
	}
	complete := request(t, f, http.MethodPost, "/api/v1/review-tasks/"+strconvFormat(review.ID)+"/complete", `{"mastery":3}`, map[string]string{"Content-Type": "application/json"})
	if complete.Code != 200 {
		t.Fatalf("complete=%d %s", complete.Code, complete.Body)
	}
	duplicate := request(t, f, http.MethodPost, "/api/v1/review-tasks/"+strconvFormat(review.ID)+"/complete", `{"mastery":3}`, map[string]string{"Content-Type": "application/json"})
	if duplicate.Code != http.StatusConflict || !strings.Contains(duplicate.Body.String(), "REVIEW_NOT_DUE") {
		t.Fatalf("duplicate complete=%d %s", duplicate.Code, duplicate.Body)
	}
	skipReview := domain.ReviewTask{ReportID: report.ID, Mastery: 2, Status: "scheduled", DueAt: now.Add(-time.Minute), CreatedAt: now, UpdatedAt: now}
	if err := f.store.DB.Create(&skipReview).Error; err != nil {
		t.Fatal(err)
	}
	skip := request(t, f, http.MethodPost, "/api/v1/review-tasks/"+strconvFormat(skipReview.ID)+"/skip", `{}`, map[string]string{"Content-Type": "application/json"})
	if skip.Code != 200 {
		t.Fatalf("skip=%d %s", skip.Code, skip.Body)
	}
	var events []domain.ReviewEvent
	f.store.DB.Order("id").Find(&events)
	if len(events) != 2 || events[0].Action != "complete" || events[1].Action != "skip" || events[1].OldMastery != 2 || events[1].NewMastery != 2 {
		t.Fatalf("events=%#v", events)
	}
}

func TestReadinessReportsDependencyFailure(t *testing.T) {
	f := setup(t)
	f.handler = api.New(f.store, skill.New(model.Fake{}), api.WithHealthChecks(
		func(context.Context) error { return nil },
		func(context.Context) error { return fmt.Errorf("qdrant unavailable") },
	), api.WithWorkerCheck(func(context.Context) (bool, error) {
		return false, nil
	})).Handler()
	ready := request(t, f, http.MethodGet, "/health/ready", "", nil)
	if ready.Code != 503 || !strings.Contains(ready.Body.String(), `"qdrant":"unavailable"`) || !strings.Contains(ready.Body.String(), `"worker":"unavailable"`) {
		t.Fatalf("ready=%d %s", ready.Code, ready.Body)
	}
}

func TestDocumentValidationAndRAGNoEvidence(t *testing.T) {
	f := setup(t)
	list := request(t, f, http.MethodGet, "/api/v1/documents", "", nil)
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"data":[]`) {
		t.Fatalf("empty document list must be an array: status=%d body=%s", list.Code, list.Body)
	}
	noEvidence := request(t, f, http.MethodPost, "/api/v1/rag/query", `{"question":"不存在的证据"}`, map[string]string{"Content-Type": "application/json"})
	if noEvidence.Code != http.StatusNotFound || !strings.Contains(noEvidence.Body.String(), "RAG_NO_EVIDENCE") {
		t.Fatalf("no evidence=%d %s", noEvidence.Code, noEvidence.Body)
	}
	for _, item := range []struct {
		name, content string
	}{{"notes.md", "# Redis\nRedis ZSet 用于任务调度。"}, {"notes.txt", "Go context controls timeout."}, {"notes.json", `{"topic":"MySQL transaction"}`}} {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		part, _ := writer.CreateFormFile("file", item.name)
		_, _ = part.Write([]byte(item.content))
		_ = writer.Close()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/documents", &body)
		req.Header.Set("Content-Type", writer.FormDataContentType())
		f.handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("%s status=%d body=%s", item.name, rec.Code, rec.Body)
		}
	}
	var uploaded, tasks int64
	f.store.DB.Table("documents").Where("status='uploaded'").Count(&uploaded)
	f.store.DB.Model(&domain.AITask{}).Where("kind='document_index' AND status='pending'").Count(&tasks)
	if uploaded != 3 || tasks != 3 {
		t.Fatalf("uploaded=%d indexing tasks=%d", uploaded, tasks)
	}
	var document domain.Document
	if err := f.store.DB.Order("id").First(&document).Error; err != nil {
		t.Fatal(err)
	}
	detail := request(t, f, http.MethodGet, "/api/v1/tasks/"+strconvFormat(document.IndexingTaskID)+"/detail", "", nil)
	if detail.Code != 200 || !strings.Contains(detail.Body.String(), `"kind":"document_index"`) || !strings.Contains(detail.Body.String(), `"document":`) {
		t.Fatalf("document detail=%d %s", detail.Code, detail.Body)
	}
	if err := f.store.DispatchOutbox(context.Background(), func(context.Context, uint64, time.Time) error { return nil }, 10); err != nil {
		t.Fatal(err)
	}
	deleted := request(t, f, http.MethodDelete, "/api/v1/documents/"+strconvFormat(document.ID), "", nil)
	if deleted.Code != 200 {
		t.Fatalf("delete document=%d %s", deleted.Code, deleted.Body)
	}
	var canceled domain.AITask
	if err := f.store.DB.First(&canceled, document.IndexingTaskID).Error; err != nil || canceled.Status != domain.TaskDead || canceled.LastError != "document deleted by user" {
		t.Fatalf("canceled task=%#v err=%v", canceled, err)
	}

	processingDocument, processingTask, err := f.store.CreateDocument(context.Background(), domain.Document{
		Filename: "processing.md", MediaType: "md", ContentHash: "processing", Content: "# processing",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.DispatchOutbox(context.Background(), func(context.Context, uint64, time.Time) error { return nil }, 10); err != nil {
		t.Fatal(err)
	}
	acquired, ok, err := f.store.Acquire(context.Background(), processingTask.ID, "delete-token", time.Now().Add(time.Minute))
	if err != nil || !ok {
		t.Fatalf("acquire=%v err=%v", ok, err)
	}
	if err := f.store.DB.Model(&domain.Document{}).Where("id=?", processingDocument.ID).Update("status", "indexing").Error; err != nil {
		t.Fatal(err)
	}
	deleted = request(t, f, http.MethodDelete, "/api/v1/documents/"+strconvFormat(processingDocument.ID), "", nil)
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete processing document=%d %s", deleted.Code, deleted.Body)
	}
	var attempt domain.TaskAttempt
	if err := f.store.DB.Where("task_id=? AND execution_generation=? AND lease_token=?",
		acquired.ID, acquired.ExecutionGeneration, "delete-token").First(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	if attempt.Status != "dead" || attempt.FinishedAt == nil || attempt.ErrorMessage != "document deleted by user" {
		t.Fatalf("attempt=%#v", attempt)
	}
}

func TestSkillRunPersistsAgentStepToolAndTraceEnvelope(t *testing.T) {
	f := setup(t)
	run := request(t, f, http.MethodPost, "/api/v1/skills/project-explanation/runs", `{"topic":"outbox"}`, map[string]string{"Content-Type": "application/json"})
	if run.Code != 200 || !strings.Contains(run.Body.String(), "agent_run_id") {
		t.Fatalf("run=%d %s", run.Code, run.Body)
	}
	var runs, steps, tools int64
	f.store.DB.Table("agent_runs").Count(&runs)
	f.store.DB.Table("agent_steps").Count(&steps)
	f.store.DB.Table("tool_calls").Count(&tools)
	if runs != 1 || steps != 1 || tools != 1 {
		t.Fatalf("runs=%d steps=%d tools=%d", runs, steps, tools)
	}
	workflow := request(t, f, http.MethodPost, "/api/v1/skills/multi-agent/runs", `{"modules":["algorithm"]}`, map[string]string{"Content-Type": "application/json"})
	if workflow.Code != 200 || !strings.Contains(workflow.Body.String(), "algorithm-diagnosis") {
		t.Fatalf("workflow=%d %s", workflow.Code, workflow.Body)
	}
}

func strconvFormat(id uint64) string {
	return fmt.Sprintf("%d", id)
}

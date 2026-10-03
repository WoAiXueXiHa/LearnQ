//go:build integration

package store_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/WoAiXueXiHa/LearnQ/internal/domain"
)

func TestPracticeConcurrentSubmissionFreezesAnswersAndCreatesOneTaskPerQuestion(t *testing.T) {
	s := database(t)
	ctx := context.Background()
	now := time.Now().UTC()
	doc := domain.Document{Filename: "practice.md", MediaType: "text/markdown", Content: "事务", ContentHash: "practice-test", Status: "ready", CreatedAt: now, UpdatedAt: now}
	if err := s.DB.Create(&doc).Error; err != nil {
		t.Fatal(err)
	}
	index := domain.DocumentIndex{DocumentID: doc.ID, TaskID: doc.ID + 1000000, ExecutionGeneration: 1, AttemptNo: 1, Content: doc.Content, ContentHash: doc.ContentHash, IndexVersion: "test", ChunkVersion: "test", Dimension: 1, Status: "active", CreatedAt: now}
	if err := s.DB.Create(&index).Error; err != nil {
		t.Fatal(err)
	}
	set := domain.QuestionSet{DocumentID: doc.ID, IndexID: index.ID, Status: "draft", Model: "fake", PromptVersion: "test", OriginalJSON: "{}", CreatedAt: now, UpdatedAt: now}
	if err := s.DB.Create(&set).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Clean dependencies before the shared helper removes feedback tasks.
		for _, query := range []string{
			"DELETE f FROM answer_feedback f JOIN practice_attempts a ON a.id=f.practice_attempt_id WHERE a.question_set_id=?",
			"DELETE FROM practice_attempts WHERE question_set_id=?",
			"DELETE FROM practice_questions WHERE question_set_id=?",
			"DELETE FROM question_sets WHERE id=?",
		} {
			if err := s.DB.Exec(query, set.ID).Error; err != nil {
				t.Error(err)
			}
		}
		s.DB.Delete(&index)
		s.DB.Delete(&doc)
	})
	if _, err := s.StartPractice(ctx, set.ID); err == nil {
		t.Fatal("draft question set accepted")
	}
	for ordinal := 1; ordinal <= 5; ordinal++ {
		question := domain.PracticeQuestion{QuestionSetID: set.ID, Ordinal: ordinal, Prompt: "问题", KnowledgePointsJSON: "[]", ReferencePointsJSON: "[]", EvidenceJSON: "{}"}
		if err := s.DB.Create(&question).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DB.Model(&set).Update("status", "confirmed").Error; err != nil {
		t.Fatal(err)
	}
	attempt, err := s.StartPractice(ctx, set.ID)
	if err != nil {
		t.Fatal(err)
	}
	partial := []string{"草稿", "", "", "", ""}
	if _, err := s.SavePracticeAnswers(ctx, attempt.ID, partial, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SavePracticeAnswers(ctx, attempt.ID, partial, true); err == nil {
		t.Fatal("partial answers submitted")
	}
	var count int64
	if err := s.DB.Model(&domain.AnswerFeedback{}).Where("practice_attempt_id=?", attempt.ID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("partial submission created %d feedbacks", count)
	}
	answers := []string{"答案一", "答案二", "答案三", "答案四", "答案五"}
	start := make(chan struct{})
	results := make(chan error, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := s.SavePracticeAnswers(ctx, attempt.ID, answers, true)
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("submission winners=%d, want 1", winners)
	}
	var feedbacks []domain.AnswerFeedback
	if err := s.DB.Where("practice_attempt_id=?", attempt.ID).Order("ordinal").Find(&feedbacks).Error; err != nil {
		t.Fatal(err)
	}
	if len(feedbacks) != 5 {
		t.Fatalf("feedback count=%d", len(feedbacks))
	}
	for n, feedback := range feedbacks {
		var task domain.AITask
		if err := s.DB.First(&task, feedback.TaskID).Error; err != nil {
			t.Fatal(err)
		}
		var payload struct {
			PracticeAttemptID uint64 `json:"practice_attempt_id"`
			Ordinal           int    `json:"ordinal"`
		}
		if err := json.Unmarshal([]byte(task.PayloadJSON), &payload); err != nil {
			t.Fatal(err)
		}
		if feedback.Ordinal != n+1 || task.Kind != "practice_feedback" || payload.PracticeAttemptID != attempt.ID || payload.Ordinal != n+1 {
			t.Fatalf("feedback/task identity mismatch: %+v %+v", feedback, task)
		}
		if err := s.DB.Model(&domain.OutboxEvent{}).Where("aggregate_id=?", task.ID).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("task %d outbox count=%d", task.ID, count)
		}
	}
	if _, err := s.SavePracticeAnswers(ctx, attempt.ID, partial, false); err == nil {
		t.Fatal("submitted answers modified")
	}
	var frozen domain.PracticeAttempt
	if err := s.DB.First(&frozen, attempt.ID).Error; err != nil {
		t.Fatal(err)
	}
	var frozenAnswers []string
	if err := json.Unmarshal([]byte(frozen.AnswersJSON), &frozenAnswers); err != nil {
		t.Fatal(err)
	}
	if frozen.Status != "feedback_pending" || frozen.SubmittedAt == nil || !reflect.DeepEqual(frozenAnswers, answers) {
		t.Fatalf("submission not frozen: %+v", frozen)
	}
	second, err := s.StartPractice(ctx, set.ID)
	if err != nil {
		t.Fatal(err)
	}
	var secondAnswers []string
	if err := json.Unmarshal([]byte(second.AnswersJSON), &secondAnswers); err != nil {
		t.Fatal(err)
	}
	if second.ID == attempt.ID || !reflect.DeepEqual(secondAnswers, []string{"", "", "", "", ""}) {
		t.Fatalf("practice reused prior answers: %+v", second)
	}
	t.Run("feedback and authority evidence remain transactional", func(t *testing.T) {
		if err := s.DispatchOutbox(ctx, func(context.Context, uint64, time.Time) error { return nil }, 100); err != nil {
			t.Fatal(err)
		}
		for n, feedback := range feedbacks {
			token := fmt.Sprintf("practice-feedback-%d", n)
			task, acquired, err := s.Acquire(ctx, feedback.TaskID, token, time.Now().UTC().Add(time.Minute))
			if err != nil || !acquired {
				t.Fatalf("acquire: %v, %v", acquired, err)
			}
			invalid := []domain.FeedbackItem{{Type: "omission", JudgmentStatus: "article_only", Explanation: "伪造引用", ChunkIDs: []string{"unprovided"}}}
			if err := s.CompleteFeedback(ctx, task, token, "{}", "fake", invalid); err == nil {
				t.Fatal("unprovided evidence accepted")
			}
			var unchanged domain.AITask
			if err := s.DB.First(&unchanged, task.ID).Error; err != nil {
				t.Fatal(err)
			}
			if unchanged.Status != domain.TaskProcessing {
				t.Fatal("invalid result consumed task lease")
			}
			items := []domain.FeedbackItem{{Type: "expression", JudgmentStatus: "unable_to_judge", Explanation: "先说明事务边界"}}
			if n == 0 {
				items = append(items, domain.FeedbackItem{Type: "conflict", JudgmentStatus: "pending_verification", Explanation: "合成待核查断言"})
			}
			if err := s.CompleteFeedback(ctx, task, token, "{}", "fake", items); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.DB.First(&frozen, attempt.ID).Error; err != nil {
			t.Fatal(err)
		}
		if frozen.Status != "feedback_ready" {
			t.Fatalf("feedback not complete: %s", frozen.Status)
		}
		if err := s.ValidatePracticeReview(ctx, attempt.ID); err == nil {
			t.Fatal("unverified conflict admitted to review")
		}
		var claim domain.AuthorityClaim
		if err := s.DB.Where("answer_feedback_id=?", feedbacks[0].ID).First(&claim).Error; err != nil {
			t.Fatal(err)
		}
		if claim.ItemOrdinal != 1 {
			t.Fatal("claim ordinal must match conflict-first stored feedback")
		}
		source := domain.AuthoritySource{Topic: "synthetic", URL: "https://example.invalid/fixture", Hostname: "example.invalid", Status: "active", CreatedAt: now}
		if err := s.DB.Create(&source).Error; err != nil {
			t.Fatal(err)
		}
		content := "仅用于数据库契约的合成版本依据，不能认领官方事实。"
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(content)))
		snapshot := domain.AuthoritySnapshot{AuthoritySourceID: source.ID, FinalURL: source.URL, Title: "synthetic fixture", VersionLabel: "synthetic-v1", Content: content, ExtractedText: content, ContentHash: hash, TextHash: hash, AccessedAt: now, ExpiresAt: now.Add(time.Hour)}
		if err := s.DB.Create(&snapshot).Error; err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			for _, query := range []string{
				"DELETE r FROM authority_check_reviews r JOIN authority_checks c ON c.id=r.authority_check_id WHERE c.authority_claim_id=?",
				"DELETE FROM authority_checks WHERE authority_claim_id=?",
				"DELETE FROM authority_claims WHERE id=?",
			} {
				if err := s.DB.Exec(query, claim.ID).Error; err != nil {
					t.Error(err)
				}
			}
			s.DB.Delete(&snapshot)
			s.DB.Delete(&source)
		})
		if _, err := s.QueueAuthorityCheck(ctx, claim.ID, snapshot.ID, "不存在的摘录", "synthetic-v1"); err == nil {
			t.Fatal("fabricated authority excerpt accepted")
		}
		checks := make(chan domain.AuthorityCheck, 8)
		failures := make(chan error, 8)
		var queueWG sync.WaitGroup
		for range 8 {
			queueWG.Add(1)
			go func() {
				defer queueWG.Done()
				check, err := s.QueueAuthorityCheck(ctx, claim.ID, snapshot.ID, content, "synthetic-v1")
				checks <- check
				failures <- err
			}()
		}
		queueWG.Wait()
		close(checks)
		close(failures)
		for err := range failures {
			if err != nil {
				t.Fatal(err)
			}
		}
		var checkID, taskID uint64
		for check := range checks {
			if checkID == 0 {
				checkID, taskID = check.ID, check.TaskID
			}
			if check.ID != checkID || check.TaskID != taskID {
				t.Fatal("duplicate authority checks/tasks created")
			}
		}
		if err := s.DispatchOutbox(ctx, func(context.Context, uint64, time.Time) error { return nil }, 100); err != nil {
			t.Fatal(err)
		}
		task, acquired, err := s.Acquire(ctx, taskID, "authority-fixture", time.Now().UTC().Add(time.Minute))
		if err != nil || !acquired {
			t.Fatalf("authority acquire: %v, %v", acquired, err)
		}
		if err := s.DB.Model(&snapshot).Update("text_hash", "tampered").Error; err != nil {
			t.Fatal(err)
		}
		if err := s.CompleteAuthorityCheck(ctx, task, "authority-fixture", "supported", "synthetic", "fake", "{}"); err == nil {
			t.Fatal("tampered snapshot accepted during completion")
		}
		if err := s.DB.Model(&snapshot).Update("text_hash", hash).Error; err != nil {
			t.Fatal(err)
		}
		if err := s.CompleteAuthorityCheck(ctx, task, "authority-fixture", "supported", "synthetic", "fake", "{}"); err != nil {
			t.Fatal(err)
		}
		if err := s.ValidatePracticeReview(ctx, attempt.ID); err == nil {
			t.Fatal("unconfirmed authority judgment admitted to review")
		}
		review := domain.AuthorityCheckReview{AuthorityCheckID: checkID, Disposition: "confirmed", Comment: "合成验收", CreatedAt: now}
		if err := s.DB.Create(&review).Error; err != nil {
			t.Fatal(err)
		}
		if err := s.ValidatePracticeReview(ctx, attempt.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.DB.Model(&snapshot).Update("expires_at", now.Add(-time.Hour)).Error; err != nil {
			t.Fatal(err)
		}
		if err := s.ValidatePracticeReview(ctx, attempt.ID); err == nil {
			t.Fatal("expired authority evidence admitted to review")
		}
	})
}

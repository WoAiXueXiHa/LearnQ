//go:build integration

package store_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/WoAiXueXiHa/LearnQ/internal/domain"
	"github.com/WoAiXueXiHa/LearnQ/internal/store"
)

func TestStructuredReferencePersistenceAndTamperRejection(t *testing.T) {
	s := database(t)
	ctx := context.Background()
	now := time.Now().UTC()
	hash := func(text string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(text))) }
	source := "# 中文\r\n事务与回滚。\r\n"
	doc := domain.Document{Filename: "reference-contract.md", MediaType: "md", Content: source, ContentHash: hash(source), Status: "ready", CreatedAt: now, UpdatedAt: now}
	if err := s.DB.Create(&doc).Error; err != nil {
		t.Fatal(err)
	}
	version := domain.DocumentIndex{DocumentID: doc.ID, TaskID: doc.ID + 4000000, ExecutionGeneration: 1, AttemptNo: 1, Content: source, ContentHash: hash(source), IndexVersion: "test", ChunkVersion: "test", Dimension: 1, Status: "active", CreatedAt: now}
	if err := s.DB.Create(&version).Error; err != nil {
		t.Fatal(err)
	}
	chunk := domain.DocumentChunk{ID: hash(fmt.Sprintf("structured-%d", version.ID)), DocumentID: doc.ID, IndexID: version.ID, Content: source, ContentHash: hash(source), StartByte: 0, EndByte: len(source), StartLine: 1, EndLine: 2, CreatedAt: now}
	if err := s.DB.Create(&chunk).Error; err != nil {
		t.Fatal(err)
	}
	set := domain.QuestionSet{DocumentID: doc.ID, IndexID: version.ID, Status: "draft", Model: "fake", PromptVersion: "v2", OriginalJSON: "{}", CreatedAt: now, UpdatedAt: now}
	if err := s.DB.Create(&set).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.DB.Where("question_set_id=?", set.ID).Delete(&domain.QuestionSetEdit{})
		s.DB.Where("question_set_id=?", set.ID).Delete(&domain.PracticeQuestion{})
		s.DB.Delete(&set)
		s.DB.Delete(&chunk)
		s.DB.Delete(&version)
		s.DB.Delete(&doc)
	})
	qs := make([]domain.QuestionDraft, 5)
	for i := range qs {
		qs[i] = domain.QuestionDraft{Prompt: fmt.Sprintf("问题%d", i), KnowledgePoints: []string{"事务"}, ReferencePoints: []string{"整体操作", "失败回滚"}, ChunkIDs: []string{chunk.ID}, ReferenceItems: []domain.ReferenceItem{{Text: "整体操作", ChunkIDs: []string{chunk.ID}}, {Text: "失败回滚", ChunkIDs: []string{chunk.ID}}}}
	}
	if _, err := s.EditQuestionSet(ctx, set.ID, qs, false); err != nil {
		t.Fatal(err)
	}
	// Learner/authoring JSON cannot serialize answer fields.
	var rows []domain.PracticeQuestion
	s.DB.Where("question_set_id=?", set.ID).Order("ordinal").Find(&rows)
	if len(rows) != 5 {
		t.Fatal("missing questions")
	}
	for _, q := range rows {
		var items []domain.ReferenceItem
		if json.Unmarshal([]byte(q.ReferenceItemsJSON), &items) != nil || len(items) != 2 {
			t.Fatal("structured points not stored")
		}
	}
	// A normal prompt-only edit restores the private structured points.
	for i := range qs {
		qs[i].Prompt += "（编辑）"
		qs[i].ReferencePoints = nil
		qs[i].ReferenceItems = nil
	}
	if _, err := s.EditQuestionSet(ctx, set.ID, qs, false); err != nil {
		t.Fatal(err)
	}
	s.DB.Where("question_set_id=?", set.ID).Order("ordinal").Find(&rows)
	for _, q := range rows {
		var items []domain.ReferenceItem
		if json.Unmarshal([]byte(q.ReferenceItemsJSON), &items) != nil || len(items) != 2 {
			t.Fatal("editing erased structured points")
		}
	}
	for _, tamper := range []struct {
		column    string
		bad, good any
	}{{"content_hash", "tampered", chunk.ContentHash}, {"start_byte", 1, 0}, {"end_line", 3, 2}, {"index_id", version.ID + 9999, version.ID}} {
		t.Run(tamper.column, func(t *testing.T) {
			s.DB.Model(&chunk).Update(tamper.column, tamper.bad)
			if _, err := s.EditQuestionSet(ctx, set.ID, qs, true); !store.IsResultValidationError(err) {
				t.Fatalf("tampered source accepted: %v", err)
			}
			s.DB.Model(&chunk).Update(tamper.column, tamper.good)
		})
	}
	if _, err := s.EditQuestionSet(ctx, set.ID, qs, true); err != nil {
		t.Fatal(err)
	}
}

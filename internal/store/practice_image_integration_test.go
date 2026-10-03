//go:build integration

package store_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"
	"time"

	"github.com/WoAiXueXiHa/LearnQ/internal/domain"
	"github.com/WoAiXueXiHa/LearnQ/internal/store"
)

func TestPracticeImageEvidenceRejectsTamperingAndFreezesCorrections(t *testing.T) {
	s := database(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	doc := domain.Document{Filename: "image-contract.md", MediaType: "text/markdown", Content: "合成图片引用", ContentHash: "image-contract", Status: "ready", CreatedAt: now, UpdatedAt: now}
	if err := s.DB.Create(&doc).Error; err != nil {
		t.Fatal(err)
	}
	version := domain.DocumentIndex{DocumentID: doc.ID, TaskID: doc.ID + 2000000, ExecutionGeneration: 1, AttemptNo: 1, Content: doc.Content, ContentHash: doc.ContentHash, IndexVersion: "test", ChunkVersion: "test", Dimension: 1, Status: "active", CreatedAt: now}
	if err := s.DB.Create(&version).Error; err != nil {
		t.Fatal(err)
	}
	original := `{"summary":"合成原始图片描述"}`
	image := domain.Image{OriginalFilename: "synthetic.png", MediaType: "image/png", ContentHash: fmt.Sprintf("%x", sha256.Sum256([]byte("synthetic image identity"))), StoragePath: "unused-contract-fixture", Status: "ready", DescriptionJSON: original, DescriptionModel: "fake", CreatedAt: now, UpdatedAt: now}
	if err := s.DB.Create(&image).Error; err != nil {
		t.Fatal(err)
	}
	ref := domain.ArticleImage{DocumentID: doc.ID, IndexID: version.ID, ImageID: &image.ID, Status: "snapshot_ready", OriginalURL: "https://example.invalid/synthetic.png", CreatedAt: now, UpdatedAt: now}
	if err := s.DB.Create(&ref).Error; err != nil {
		t.Fatal(err)
	}
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(original)))
	evidence := domain.ImageEvidence{ID: fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("fixture-%d", ref.ID)))), ArticleImageID: ref.ID, DocumentID: doc.ID, IndexID: version.ID, ImageID: image.ID, ImageHash: image.ContentHash, Content: original, ContentHash: hash, Status: "ready", CreatedAt: now, UpdatedAt: now}
	if err := s.DB.Create(&evidence).Error; err != nil {
		t.Fatal(err)
	}
	set := domain.QuestionSet{DocumentID: doc.ID, IndexID: version.ID, Status: "draft", Model: "fake", PromptVersion: "test", OriginalJSON: "{}", CreatedAt: now, UpdatedAt: now}
	if err := s.DB.Create(&set).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, row := range []any{&domain.QuestionSetEdit{}, &domain.PracticeQuestion{}} {
			if err := s.DB.Where("question_set_id=?", set.ID).Delete(row).Error; err != nil {
				t.Error(err)
			}
		}
		s.DB.Delete(&set)
		s.DB.Where("document_id=?", doc.ID).Delete(&store.ImageDescriptionCorrection{})
		s.DB.Delete(&evidence)
		s.DB.Delete(&ref)
		s.DB.Delete(&image)
		s.DB.Delete(&version)
		s.DB.Delete(&doc)
	})
	questions := make([]domain.QuestionDraft, 5)
	for n := range questions {
		questions[n] = domain.QuestionDraft{Prompt: fmt.Sprintf("图片问题%d", n+1), KnowledgePoints: []string{"图中节点"}, ReferencePoints: []string{"合成依据"}, ImageRefIDs: []uint64{ref.ID}}
	}
	if _, err := s.EditQuestionSet(ctx, set.ID, questions, false); err != nil {
		t.Fatal(err)
	}
	for _, tamper := range []struct {
		column    string
		bad, good any
	}{
		{"content_hash", "tampered", hash},
		{"image_hash", "tampered", image.ContentHash},
		{"index_id", version.ID + 999999, version.ID},
		{"status", "failed", "ready"},
	} {
		t.Run(tamper.column, func(t *testing.T) {
			if err := s.DB.Model(&evidence).Update(tamper.column, tamper.bad).Error; err != nil {
				t.Fatal(err)
			}
			if _, err := s.EditQuestionSet(ctx, set.ID, questions, true); !store.IsResultValidationError(err) {
				t.Fatalf("tampered %s accepted: %v", tamper.column, err)
			}
			var persisted domain.QuestionSet
			if err := s.DB.First(&persisted, set.ID).Error; err != nil {
				t.Fatal(err)
			}
			if persisted.Status != "draft" {
				t.Fatal("rejected confirmation changed question set")
			}
			if err := s.DB.Model(&evidence).Update(tamper.column, tamper.good).Error; err != nil {
				t.Fatal(err)
			}
		})
	}
	correction := store.ImageDescriptionCorrection{DocumentID: doc.ID, ImageHash: image.ContentHash, DescriptionJSON: `{"summary":"合成人工修订"}`, Comment: "保留历史描述", CreatedAt: now.Add(time.Second)}
	if err := s.DB.Create(&correction).Error; err != nil {
		t.Fatal(err)
	}
	content, model, err := store.EffectiveImageDescription(ctx, s.DB, version, image)
	if err != nil || content != original || model != "fake" {
		t.Fatalf("historical description changed: %s %s %v", content, model, err)
	}
	newVersion := version
	newVersion.CreatedAt = now.Add(2 * time.Second)
	content, model, err = store.EffectiveImageDescription(ctx, s.DB, newVersion, image)
	if err != nil || content != correction.DescriptionJSON || model != "user-correction" {
		t.Fatalf("new version did not apply correction: %s %s %v", content, model, err)
	}
	otherArticle := newVersion
	otherArticle.DocumentID += 999999
	content, _, err = store.EffectiveImageDescription(ctx, s.DB, otherArticle, image)
	if err != nil || content != original {
		t.Fatal("correction leaked to another article")
	}
	if _, err := s.EditQuestionSet(ctx, set.ID, questions, true); err != nil {
		t.Fatal(err)
	}
	var retained domain.ImageEvidence
	if err := s.DB.First(&retained, "id=?", evidence.ID).Error; err != nil {
		t.Fatal(err)
	}
	if retained.Content != original {
		t.Fatal("historical indexed evidence overwritten")
	}
}

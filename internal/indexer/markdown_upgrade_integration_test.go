//go:build integration

package indexer_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/WoAiXueXiHa/LearnQ/internal/domain"
	"github.com/WoAiXueXiHa/LearnQ/internal/model"
	"github.com/WoAiXueXiHa/LearnQ/internal/rag"
)

type recordingStructureEmbedding struct {
	texts []string
	fail  bool
}

func (m *recordingStructureEmbedding) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	m.texts = append([]string{}, texts...)
	if m.fail {
		return nil, errors.New("controlled embedding failure")
	}
	return model.Fake{Dimension: 64}.Embed(ctx, texts)
}

func TestMarkdownUpgradePersistsRawEvidenceAndEmbedsCleanText(t *testing.T) {
	s, p, old, _ := versionFixture(t)
	var priorChunks []domain.DocumentChunk
	if err := s.DB.Where("document_id=? AND index_id=?", old.ID, old.ActiveIndexID).Find(&priorChunks).Error; err != nil {
		t.Fatal(err)
	}
	var priorVersion domain.DocumentIndex
	if err := s.DB.First(&priorVersion, old.ActiveIndexID).Error; err != nil {
		t.Fatal(err)
	}
	if priorVersion.ChunkVersion != rag.LegacyChunkVersion {
		t.Fatalf("fixture not legacy: %#v", priorVersion)
	}
	// Change the new upload input only; the old snapshot remains immutable.
	source := "# **机制**\r\n\r\n先执行 **fork**。\r\n\r\n再写入 [原子文件](https://example.test/docs)。\r\n\r\n![流程图](https://example.test/flow.png)\r\n"
	if err := s.DB.Model(&domain.Document{}).Where("id=?", old.ID).Updates(map[string]any{"content": source, "content_hash": hashStructureSource(source)}).Error; err != nil {
		t.Fatal(err)
	}
	_, task, err := s.ReindexDocument(context.Background(), old.ID)
	if err != nil {
		t.Fatal(err)
	}
	task = acquireVersionTask(t, s, task, "structure-upgrade")
	recorder := &recordingStructureEmbedding{}
	p.Embedding = recorder
	p.ChunkVersion = rag.MarkdownChunkVersion
	if _, err := p.Process(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteDocumentIndex(context.Background(), task, "structure-upgrade", old.ID, p.IndexVersion()); err != nil {
		t.Fatal(err)
	}
	var current domain.Document
	if err := s.DB.First(&current, old.ID).Error; err != nil {
		t.Fatal(err)
	}
	var version domain.DocumentIndex
	if err := s.DB.First(&version, current.ActiveIndexID).Error; err != nil {
		t.Fatal(err)
	}
	if version.ChunkVersion != rag.MarkdownChunkVersion || version.Content != source || version.ID == priorVersion.ID {
		t.Fatalf("wrong structure version %#v", version)
	}
	var actual []domain.DocumentChunk
	if err := s.DB.Where("index_id=?", version.ID).Order("chunk_index").Find(&actual).Error; err != nil {
		t.Fatal(err)
	}
	expected, err := rag.ChunkMarkdown(source)
	if err != nil {
		t.Fatal(err)
	}
	if len(actual) != len(expected) || len(recorder.texts) != len(expected) {
		t.Fatalf("rows=%d expected=%d embeddings=%d", len(actual), len(expected), len(recorder.texts))
	}
	changed := false
	for i, c := range actual {
		var heading []string
		var imageRefs []rag.ImageReference
		var blockSpans []rag.MarkdownBlock
		if err := json.Unmarshal([]byte(c.HeadingPathJSON), &heading); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(c.ImageRefsJSON), &imageRefs); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(c.BlockSpansJSON), &blockSpans); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(heading, expected[i].HeadingPath) || c.BlockType != expected[i].BlockType || !reflect.DeepEqual(imageRefs, expected[i].ImageRefs) || len(blockSpans) == 0 {
			t.Fatalf("structural metadata not persisted %#v", c)
		}

		if c.Content != source[c.StartByte:c.EndByte] || c.ContentHash != hashStructureSource(c.Content) {
			t.Fatal("raw original evidence changed")
		}
		if recorder.texts[i] != expected[i].EmbeddingContent {
			t.Fatalf("embedding not clean chunk text: %q vs %q", recorder.texts[i], expected[i].EmbeddingContent)
		}
		if recorder.texts[i] != c.Content {
			changed = true
		}
		if strings.Contains(recorder.texts[i], "https://example.test/") {
			t.Fatal("image/link URL leaked into clean embedding text")
		}
	}
	if !changed {
		t.Fatal("embedding raw separation did not occur")
	}
	for _, prior := range priorChunks {
		var retained domain.DocumentChunk
		if err := s.DB.Where("id=?", prior.ID).First(&retained).Error; err != nil {
			t.Fatal(err)
		}
		if retained.Content != prior.Content || retained.ContentHash != prior.ContentHash {
			t.Fatal("upgrade rewrote old raw evidence")
		}
	}
	var retainedVersion domain.DocumentIndex
	if err := s.DB.First(&retainedVersion, priorVersion.ID).Error; err != nil {
		t.Fatal(err)
	}
	if retainedVersion.Content != old.Content || retainedVersion.Status != "retired" {
		t.Fatal("old source snapshot lost after structure upgrade")
	}
}

func TestMarkdownUpgradeEmbeddingFailureKeepsLegacyActive(t *testing.T) {
	s, p, old, _ := versionFixture(t)
	_, task, err := s.ReindexDocument(context.Background(), old.ID)
	if err != nil {
		t.Fatal(err)
	}
	task = acquireVersionTask(t, s, task, "structure-fail")
	p.ChunkVersion = rag.MarkdownChunkVersion
	p.Embedding = &recordingStructureEmbedding{fail: true}
	if _, err := p.Process(context.Background(), task); err == nil {
		t.Fatal("expected controlled failure")
	}
	if _, _, err := s.FailDocument(context.Background(), task, "structure-fail", errors.New("structure embed failed"), true); err != nil {
		t.Fatal(err)
	}
	var after domain.Document
	if err := s.DB.First(&after, old.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.ActiveIndexID != old.ActiveIndexID || after.Status != "ready" || after.ErrorMessage == "" {
		t.Fatalf("failed upgrade lost old active evidence %#v", after)
	}
}

func hashStructureSource(source string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(source)))
}

func TestMarkdownTextlessUpgradeKeepsRawWithoutEmbeddingOrVector(t *testing.T) {
	for name, source := range map[string]string{"long HTML": "<div>" + strings.Repeat("html-hidden", 1000) + "</div>\r\n", "HTML": "<img src=\"https://example.test/raw.png\">\r\n", "reference definition": "[unused]: https://example.test/unused.png\r\n"} {
		t.Run(name, func(t *testing.T) {
			s, p, old, _ := versionFixture(t)
			if err := s.DB.Model(&domain.Document{}).Where("id=?", old.ID).Updates(map[string]any{"content": source, "content_hash": hashStructureSource(source)}).Error; err != nil {
				t.Fatal(err)
			}
			_, task, err := s.ReindexDocument(context.Background(), old.ID)
			if err != nil {
				t.Fatal(err)
			}
			task = acquireVersionTask(t, s, task, "textless")
			recorder := &recordingStructureEmbedding{}
			vectors := &memoryVectors{}
			p.Embedding = recorder
			p.Vectors = vectors
			p.ChunkVersion = rag.MarkdownChunkVersion
			if _, err := p.Process(context.Background(), task); err != nil {
				t.Fatal(err)
			}
			if err := s.CompleteDocumentIndex(context.Background(), task, "textless", old.ID, p.IndexVersion()); err != nil {
				t.Fatal(err)
			}
			if len(recorder.texts) != 0 || len(vectors.points) != 0 {
				t.Fatalf("textless source called embedding/vector: %#v %#v", recorder.texts, vectors.points)
			}
			var current domain.Document
			if err := s.DB.First(&current, old.ID).Error; err != nil {
				t.Fatal(err)
			}
			var chunks []domain.DocumentChunk
			if err := s.DB.Where("index_id=?", current.ActiveIndexID).Find(&chunks).Error; err != nil {
				t.Fatal(err)
			}
			if len(chunks) == 0 {
				t.Fatal("textless raw evidence lost")
			}
			for _, chunk := range chunks {
				if chunk.Content != source[chunk.StartByte:chunk.EndByte] || chunk.EmbeddingContent != "" {
					t.Fatalf("raw/clean separation invalid %#v", chunk)
				}
			}
		})
	}
}

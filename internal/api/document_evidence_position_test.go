package api

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/WoAiXueXiHa/LearnQ/internal/domain"
	"github.com/WoAiXueXiHa/LearnQ/internal/rag"
)

func TestEvidencePositionValidatesImmutableLocation(t *testing.T) {
	source := "标题\r\n重复中文\r\n重复中文\r\n🙂结尾"
	text := "重复中文"
	start := strings.LastIndex(source, text)
	sum := sha256.Sum256([]byte(text))
	good := domain.DocumentChunk{IndexID: 7, Content: text, ContentHash: hex.EncodeToString(sum[:]), StartByte: start, EndByte: start + len(text), StartLine: 3, EndLine: 3}
	cases := []struct {
		name   string
		mutate func(*domain.DocumentChunk)
		valid  bool
	}{
		{"second occurrence", func(*domain.DocumentChunk) {}, true},
		{"wrong repeated occurrence", func(c *domain.DocumentChunk) {
			c.StartByte = strings.Index(source, text)
			c.EndByte = c.StartByte + len(text)
		}, false},
		{"negative", func(c *domain.DocumentChunk) { c.StartByte = -1 }, false},
		{"empty", func(c *domain.DocumentChunk) { c.EndByte = c.StartByte }, false},
		{"overflow", func(c *domain.DocumentChunk) { c.EndByte = len(source) + 1 }, false},
		{"split utf8", func(c *domain.DocumentChunk) { c.StartByte++ }, false},
		{"wrong content", func(c *domain.DocumentChunk) { c.Content = "伪造" }, false},
		{"wrong hash", func(c *domain.DocumentChunk) { c.ContentHash = strings.Repeat("0", 64) }, false},
		{"wrong end line", func(c *domain.DocumentChunk) { c.EndLine++ }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			chunk := good
			tc.mutate(&chunk)
			a, b, err := evidenceChunkPosition(source, chunk)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
			if tc.valid && (a != start || b != start+len(text)) {
				t.Fatalf("wrong original bytes %d:%d", a, b)
			}
		})
	}
}

func TestEvidencePositionLegacyRequiresHistoricalProof(t *testing.T) {
	source := strings.Repeat("中文\r\n", 600)
	chunks := rag.ChunkText(source, rag.DefaultChunkSize, rag.DefaultChunkOverlap)
	last := chunks[len(chunks)-1]
	legacy := domain.DocumentChunk{IndexID: 0, ChunkIndex: last.Index, Content: last.Content, ContentHash: last.Hash, StartByte: -1, EndByte: -1, StartLine: last.StartLine, EndLine: last.EndLine}
	a, b, err := evidenceChunkPosition(source, legacy)
	if err != nil || source[a:b] != last.Content {
		t.Fatalf("legacy reference cannot be explained: %d:%d %v", a, b, err)
	}
	legacy.ChunkIndex = -1
	if _, _, err := evidenceChunkPosition(source, legacy); err == nil {
		t.Fatal("invalid legacy index accepted")
	}
	legacy.ChunkIndex = last.Index
	legacy.Content += "changed"
	if _, _, err := evidenceChunkPosition(source, legacy); err == nil {
		t.Fatal("unprovable legacy evidence accepted")
	}
}

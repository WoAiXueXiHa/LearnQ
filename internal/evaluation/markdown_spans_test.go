package evaluation

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/WoAiXueXiHa/LearnQ/internal/rag"
)

func TestMarkdownEvidenceCrossParagraphAndImageDefinitionSpans(t *testing.T) {
	source := "# 因果链\r\n\r\nfork产生子进程。\r\n\r\n子进程写临时文件。\r\n\r\n原子替换文件。\r\n\r\n![流程][flow]\r\n\r\n[flow]: https://example.test/flow.png\r\n"
	chunks, err := rag.ChunkMarkdown(source)
	if err != nil {
		t.Fatal(err)
	}
	rows := make([]EvidenceChunk, len(chunks))
	for i, c := range chunks {
		start := c.StartByte
		rows[i] = EvidenceChunk{ID: fmt.Sprintf("structure-%d", i), Content: c.Content, StartLine: c.StartLine, EndLine: c.EndLine, StartByte: &start}
	}
	cases := []Case{{ID: "cross-paragraph", Question: "解释完整因果链", DocumentID: 7, ArticleSHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(source))), EvidencePoints: []EvidencePoint{{Point: "含空行的跨段机制", Lines: [][]int{{3, 7}}}, {Point: "图片引用与定义", Lines: [][]int{{9, 11}}}}}}
	resolved, err := ResolveEvidencePoints(context.Background(), cases, func(context.Context, uint64) (string, []EvidenceChunk, error) { return source, rows, nil })
	if err != nil {
		t.Fatalf("structure raw coverage failed: %v", err)
	}
	for _, point := range resolved[0].EvidencePoints {
		if len(point.ChunkIDs) == 0 {
			t.Fatalf("span absent: %#v", point)
		}
	}
	// Removing a genuine mechanism span must not be repaired by line overlap or identical text elsewhere.
	filtered := make([]EvidenceChunk, 0)
	for _, row := range rows {
		if !strings.Contains(row.Content, "子进程写临时文件") {
			filtered = append(filtered, row)
		}
	}
	if _, err := ResolveEvidencePoints(context.Background(), cases, func(context.Context, uint64) (string, []EvidenceChunk, error) { return source, filtered, nil }); err == nil {
		t.Fatal("incomplete mechanism was accepted")
	}
}

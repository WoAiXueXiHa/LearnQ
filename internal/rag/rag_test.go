package rag

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestChunkTextUTF8AndOverlap(t *testing.T) {
	text := "# 标题\n" + strings.Repeat("学习Go语言。", 50)
	chunks := ChunkText(text, 80, 12)
	if len(chunks) < 2 || chunks[0].StartLine != 1 || chunks[0].Title != "标题" {
		t.Fatalf("unexpected chunks: %#v", chunks)
	}
	for _, chunk := range chunks {
		if !utf8.ValidString(chunk.Content) {
			t.Fatal("invalid UTF-8")
		}
	}
}

func TestSparseChineseAndEnglish(t *testing.T) {
	got := Sparse("Redis 任务队列 redis")
	if len(got.Indices) < 4 || len(got.Indices) != len(got.Values) {
		t.Fatalf("unexpected sparse vector %#v", got)
	}
}

func TestMetrics(t *testing.T) {
	rel := map[string]int{"a": 3, "b": 1}
	if got := RecallAtK([]string{"a", "x"}, rel, 2); got != .5 {
		t.Fatalf("recall=%v", got)
	}
	if got := NDCGAtK([]string{"a", "b"}, rel, 2); got < .99 {
		t.Fatalf("ndcg=%v", got)
	}
	if got := RetrievalCoverage([]string{"a", "b"}, []string{"a"}); got != .5 {
		t.Fatalf("coverage=%v", got)
	}
}

func TestRRF(t *testing.T) {
	got := RRF([][]string{{"a", "b"}, {"b", "c"}}, 60)
	if len(got) != 3 || got[0] != "b" {
		t.Fatalf("got %#v", got)
	}
}

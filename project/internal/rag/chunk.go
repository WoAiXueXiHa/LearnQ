package rag

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode/utf8"
)

type Chunk struct {
	ID        string `json:"id"`
	Index     int    `json:"index"`
	Title     string `json:"title"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Content   string `json:"content"`
	Hash      string `json:"hash"`
}

type runeAt struct {
	value rune
	line  int
}

func ChunkText(text string, size, overlap int) []Chunk {
	// 按 rune 而非 byte 切片，避免截断 UTF-8 中文；重叠窗口保留跨块语义，
	// 行号则用于最终引用定位。当前策略刻意简单、确定，便于重建稳定索引。
	if size <= 0 {
		size = 800
	}
	if overlap < 0 || overlap >= size {
		overlap = 120
	}
	var runes []runeAt
	line := 1
	title := ""
	for _, r := range text {
		runes = append(runes, runeAt{r, line})
		if r == '\n' {
			line++
		}
	}
	for _, candidate := range strings.Split(text, "\n") {
		// 提取首个 Markdown 标题作为块级元数据，不改写正文，确保引用仍能回到原文。
		if strings.HasPrefix(strings.TrimSpace(candidate), "#") {
			title = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(candidate), "#"))
			break
		}
	}
	var out []Chunk
	for start := 0; start < len(runes); {
		end := min(start+size, len(runes))
		value := make([]rune, end-start)
		for i := start; i < end; i++ {
			value[i-start] = runes[i].value
		}
		content := string(value)
		sum := sha256.Sum256([]byte(content))
		hash := hex.EncodeToString(sum[:])
		out = append(out, Chunk{ID: hash, Index: len(out), Title: title, StartLine: runes[start].line, EndLine: runes[end-1].line, Content: content, Hash: hash})
		if end == len(runes) {
			break
		}
		start = end - overlap
	}
	return out
}

func ValidDocument(body []byte) bool { return len(body) > 0 && len(body) <= 5<<20 && utf8.Valid(body) }

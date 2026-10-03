package rag

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestChunkOffsetsLocateExactOriginalBytes(t *testing.T) {
	cases := map[string]string{
		"LF":                      "# 标题\n重复文字\n重复文字\n结束\n",
		"CRLF":                    "# 标题\r\n重复文字\r\n重复文字\r\n结束\r\n",
		"ChineseLongLine":         strings.Repeat("中文🙂重复", 1000),
		"RepeatedIdenticalChunks": strings.Repeat("同", 32),
		"SingleRune":              "🙂",
	}
	for name, original := range cases {
		t.Run(name, func(t *testing.T) {
			chunks := ChunkText(original, 8, 2)
			covered := make([]bool, len(original))
			previousStart := -1
			for _, chunk := range chunks {
				if chunk.StartByte <= previousStart || chunk.StartByte < 0 || chunk.EndByte <= chunk.StartByte || chunk.EndByte > len(original) {
					t.Fatalf("invalid original span: %#v", chunk)
				}
				if original[chunk.StartByte:chunk.EndByte] != chunk.Content || !utf8.ValidString(chunk.Content) {
					t.Fatalf("offset does not reproduce original UTF-8: %#v", chunk)
				}
				wantStartLine := 1 + strings.Count(original[:chunk.StartByte], "\n")
				_, lastRuneSize := utf8.DecodeLastRuneInString(chunk.Content)
				wantEndLine := 1 + strings.Count(original[:chunk.EndByte-lastRuneSize], "\n")
				if chunk.StartLine != wantStartLine || chunk.EndLine != wantEndLine {
					t.Fatalf("lines got %d..%d want %d..%d", chunk.StartLine, chunk.EndLine, wantStartLine, wantEndLine)
				}
				for i := chunk.StartByte; i < chunk.EndByte; i++ {
					covered[i] = true
				}
				previousStart = chunk.StartByte
			}
			for i, present := range covered {
				if !present {
					t.Fatalf("original byte %d not covered", i)
				}
			}
		})
	}
}

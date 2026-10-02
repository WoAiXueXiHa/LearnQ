package rag

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode/utf8"
)

// Chunk 是切块产物：ID 与 Hash 同为内容 SHA-256，按内容寻址，
// 同一文本块在任何位置切出都得到相同 ID；Qdrant 点主键由索引层在此
// 哈希上叠加文档 ID 与块序号派生（见 internal/indexer），并非直接用此哈希。
// Index 是块在文档内的序号，StartLine/EndLine 指向原文行号，供引用定位。
type Chunk struct {
	ID        string `json:"id"`
	Index     int    `json:"index"`
	Title     string `json:"title"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Content   string `json:"content"`
	Hash      string `json:"hash"`
}

// runeAt 记录单个 rune 及其所在行号，切块时随文本一起搬运，用于计算块的行区间。
type runeAt struct {
	value rune
	line  int
}

// 默认切块参数。索引与离线校验必须用同一组值，否则报告里的行区间对不上线上索引，
// 因此这里显式导出，而不是让调用方各写一份字面量。
const (
	DefaultChunkSize    = 800
	DefaultChunkOverlap = 120
)

// ChunkText 把整段文本切成若干个等长 Chunk 并带重叠窗口（末块可能偏短）。
// size/overlap 非正或非法时回退默认值；注意 overlap 传 0 是合法输入，表示不重叠，
// 只有越界（>= size 或为负）才触发回退。空文本返回空切片而非报错，
// 是否接受空结果由调用方决定。
func ChunkText(text string, size, overlap int) []Chunk {
	// 按 rune 而非 byte 切片，避免截断 UTF-8 中文；重叠窗口保留跨块语义，
	// 行号则用于最终引用定位。当前策略刻意简单、确定，便于重建稳定索引。
	// 参数回退：size 必须为正，overlap 必须落在 [0, size) 内，非法值统一回退默认，
	// 使所有调用方得到一致且可重建的分块结果。
	if size <= 0 {
		size = DefaultChunkSize
	}
	if overlap < 0 || overlap >= size {
		overlap = DefaultChunkOverlap
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
		// 块 ID 取内容哈希：重复内容天然得到相同 ID，按内容寻址；
		// Qdrant 去重依靠索引层叠加文档 ID 与块序号派生的稳定 ID，而非此哈希本身。
		sum := sha256.Sum256([]byte(content))
		hash := hex.EncodeToString(sum[:])
		out = append(out, Chunk{ID: hash, Index: len(out), Title: title, StartLine: runes[start].line, EndLine: runes[end-1].line, Content: content, Hash: hash})
		// 块已覆盖到文末时收尾：后面不再有未切文本，直接结束循环。
		if end == len(runes) {
			break
		}
		// 滑动窗口：下一块起点后移 overlap 个 rune，使跨块语义在相邻块中重复出现。
		start = end - overlap
	}
	return out
}

// ValidDocument 校验文档非空、不超过 5 MiB（与上传接口上限一致）且为合法 UTF-8，
// 后者保证切块按 rune 处理时不会遇到非法字节序列。
func ValidDocument(body []byte) bool { return len(body) > 0 && len(body) <= 5<<20 && utf8.Valid(body) }

package evaluation

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/WoAiXueXiHa/LearnQ/internal/rag"
)

// evalCorpus 是 data/eval/rag.jsonl 的冻结语料：RUNBOOK 的评测步骤要求先上传这两份文档，
// 再用 citation_text 解析，因此离线校验必须用同一对文件。
var evalCorpus = []string{"README.md", "RUNBOOK.md"}

// TestFrozenEvalDatasetResolvesUniquely 在离线环境复算 citation_text 的解析结果。
// 切块是 800 rune 窗口加 120 rune 重叠，落在重叠区的片段会同时命中相邻两块，
// 服务端会因此拒绝整份数据集；这个测试把这类歧义挡在提交前，而不是等到跑评估时才发现。
// 它只覆盖冻结语料，线上还有其他 ready 文档时，仍需以服务端解析结果为准。
func TestFrozenEvalDatasetResolvesUniquely(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "data", "eval", "rag.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	cases, err := ReadJSONL(strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) < 10 {
		t.Fatalf("frozen dataset shrank to %d cases", len(cases))
	}

	corpus := map[string][]rag.Chunk{}
	for _, name := range evalCorpus {
		text, err := os.ReadFile(filepath.Join("..", "..", name))
		if err != nil {
			t.Fatal(err)
		}
		corpus[name] = rag.ChunkText(string(text), rag.DefaultChunkSize, rag.DefaultChunkOverlap)
		if len(corpus[name]) == 0 {
			t.Fatalf("%s produced no chunks", name)
		}
	}

	for _, item := range cases {
		// 入库的数据集必须保持可移植：写死的 chunk id 换个数据库就指向别的块或直接失效。
		if item.CitationText == "" {
			t.Errorf("case %s ships resolved chunk ids instead of citation_text", item.ID)
			continue
		}
		// 服务端的 LOCATE 跑在 MySQL 默认的大小写不敏感排序规则下，离线校验也按小写比对，
		// 否则会漏掉只有大小写差异的重复命中。
		snippet := strings.ToLower(item.CitationText)
		var matches []string
		for name, chunks := range corpus {
			for _, chunk := range chunks {
				if strings.Contains(strings.ToLower(chunk.Content), snippet) {
					matches = append(matches, fmt.Sprintf("%s#%d(%d-%d)", name, chunk.Index, chunk.StartLine, chunk.EndLine))
				}
			}
		}
		if len(matches) != 1 {
			t.Errorf("case %s citation_text matches %d chunks %v; snippet=%q", item.ID, len(matches), matches, item.CitationText)
		}
	}
}

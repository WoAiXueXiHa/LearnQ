package evaluation

import (
	"embed"
	"strings"
)

//go:embed data/rag.jsonl
var datasets embed.FS

func DefaultCases() ([]Case, error) {
	// 固定数据集嵌入二进制，保证不同环境默认评估使用完全相同的样本版本。
	body, err := datasets.ReadFile("data/rag.jsonl")
	if err != nil {
		return nil, err
	}
	return ReadJSONL(strings.NewReader(string(body)))
}

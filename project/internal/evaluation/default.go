package evaluation

import (
	"embed"
	"strings"
)

//go:embed data/rag.jsonl
var datasets embed.FS

func DefaultCases() ([]Case, error) {
	body, err := datasets.ReadFile("data/rag.jsonl")
	if err != nil {
		return nil, err
	}
	return ReadJSONL(strings.NewReader(string(body)))
}

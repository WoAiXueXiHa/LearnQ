package evaluation

import (
	"strings"
	"testing"
)

func TestReportDoesNotGuessMissingHistoricalIndexMetadata(t *testing.T) {
	rendered := Markdown(Report{Mode: "pipeline_test", TopK: 5})
	if !strings.Contains(rendered, "未提供") || !strings.Contains(rendered, "不能由当前配置推断历史切块规则") {
		t.Fatalf("unknown index hidden: %s", rendered)
	}
	if strings.Contains(rendered, "固定 800") || strings.Contains(rendered, "800 rune") {
		t.Fatal("report fabricated old chunk strategy")
	}
}

func TestReportPreservesActualMixedIndexContracts(t *testing.T) {
	report := Report{Mode: "pipeline_test", TopK: 5, Indexes: []IndexMetadata{
		{DocumentID: 1, IndexID: 11, ArticleSHA256: strings.Repeat("a", 64), IndexVersion: "fake-rune", ChunkVersion: "rune-800-overlap-120-v1", Dimension: 64},
		{DocumentID: 2, IndexID: 22, ArticleSHA256: strings.Repeat("b", 64), IndexVersion: "fake-structure", ChunkVersion: "markdown-structure-test", Dimension: 128},
	}}
	rendered := Markdown(report)
	for _, required := range []string{"rune-800-overlap-120-v1", "markdown-structure-test", "fake-rune", "fake-structure", strings.Repeat("a", 64), strings.Repeat("b", 64), `"dimension": 64`, `"dimension": 128`} {
		if !strings.Contains(rendered, required) {
			t.Fatalf("actual index metadata %q omitted", required)
		}
	}
}

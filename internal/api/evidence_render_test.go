package api

import (
	"strings"
	"testing"
)

func TestEvidenceRenderingRejectsActiveContent(t *testing.T) {
	source := "# 原文\r\n\r\n<script>alert(1)</script>\n\n<img src=x onerror=alert(2)>\n\n![远程](https://example.com/private.png)\n\n[危险](javascript:alert%281%29)\n\n[安全](https://go.dev/)\n\n| 名称 | 值 |\n| --- | --- |\n| A | B |\n\n```go\nfmt.Println(\"中文\")\n```"
	html, err := renderEvidenceMarkdown(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, unsafe := range []string{"<script", "<img", "onerror=", "href=\"javascript:", "private.png"} {
		if strings.Contains(html, unsafe) {
			t.Fatalf("unsafe rendered content %q: %s", unsafe, html)
		}
	}
	for _, expected := range []string{"<h1>", "<table>", "<pre>", "https://go.dev/", "固定快照", "中文"} {
		if !strings.Contains(html, expected) {
			t.Fatalf("missing %q: %s", expected, html)
		}
	}
}

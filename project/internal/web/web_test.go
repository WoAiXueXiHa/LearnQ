package web

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTemplateRenders(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	body := rec.Body.String()
	for _, expected := range []string{
		"LearnQ", "最近记录", "即将复习", "上传并建立索引", "AI Skill 工作台",
		`role="tablist"`, `role="tabpanel"`, `aria-selected="true"`, `id="taskLookupForm"`,
	} {
		if rec.Code != 200 || !strings.Contains(body, expected) {
			t.Fatalf("status=%d missing=%q body=%s", rec.Code, expected, body)
		}
	}
	if strings.Contains(body, "<pre></pre>") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestStaticJavaScriptContainsSafeMarkdownAndPollingRecovery(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/static/app.js", nil))
	body := rec.Body.String()
	for _, expected := range []string{
		"escapeHTML(markdown", "taskPollFailures", "documentPollFailures",
		"healthDependencies.worker", "setButtonBusy", `task.kind === "document_index"`,
		`"multi-agent"`, "/report.md", "AbortController", "retryable(error)",
		"REQUEST_CANCELLED", "openTaskFromRecord", "loadSkills",
		"Array.isArray(due)", "Array.isArray(upcoming)", "localizeStatuses",
		"文档已就绪，请输入问题开始检索。",
		"reportPreview", `!line.includes("_tool_results")`,
		"Array.isArray(documents)",
	} {
		if rec.Code != 200 || !strings.Contains(body, expected) {
			t.Fatalf("status=%d missing=%q", rec.Code, expected)
		}
	}
}

func TestStaticCSSPreservesHiddenPanels(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/static/style.css", nil))
	body := rec.Body.String()
	for _, expected := range []string{
		"main>section[hidden]{display:none!important}",
		"scroll-margin-top",
		"focus-visible",
	} {
		if rec.Code != 200 || !strings.Contains(body, expected) {
			t.Fatalf("status=%d missing=%q", rec.Code, expected)
		}
	}
}

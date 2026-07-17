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
	for _, expected := range []string{"LearnQ", "最近记录", "即将复习", "上传并建立索引", "AI Skill 工作台"} {
		if rec.Code != 200 || !strings.Contains(body, expected) {
			t.Fatalf("status=%d missing=%q body=%s", rec.Code, expected, body)
		}
	}
	if strings.Contains(body, "<pre></pre>") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

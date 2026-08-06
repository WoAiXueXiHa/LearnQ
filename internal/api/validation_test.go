package api_test

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/WoAiXueXiHa/LearnQ/internal/api"
	"github.com/WoAiXueXiHa/LearnQ/internal/model"
	"github.com/WoAiXueXiHa/LearnQ/internal/skill"
	"github.com/WoAiXueXiHa/LearnQ/internal/store"
)

func validationHandler() http.Handler {
	return api.New(store.New(nil), skill.New(model.Fake{})).Handler()
}

func TestEvaluationRequiresExplicitDataset(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/evaluations/rag", strings.NewReader("{}"))
	validationHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "real chunk ids is required") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
}

func TestRAGAndStudyRecordRejectOversizedFields(t *testing.T) {
	tests := []struct {
		name, path, body string
	}{
		{
			name: "rag question",
			path: "/api/v1/rag/query",
			body: `{"question":"` + strings.Repeat("x", 2001) + `"}`,
		},
		{
			name: "study title",
			path: "/api/v1/study-records",
			body: `{"title":"` + strings.Repeat("x", 256) + `","duration_minutes":30,"modules":[{"category":"backend","content":"x"}]}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
			req.Header.Set("Content-Type", "application/json")
			validationHandler().ServeHTTP(rec, req)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
			}
		})
	}
}

func TestDocumentUploadEnforcesFileAndMultipartLimits(t *testing.T) {
	tests := []struct {
		name      string
		fileBytes int
		extra     int
	}{
		{name: "file limit", fileBytes: (5 << 20) + 1},
		{name: "multipart limit", fileBytes: 1, extra: 6 << 20},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var body bytes.Buffer
			writer := multipart.NewWriter(&body)
			part, err := writer.CreateFormFile("file", "notes.md")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := part.Write(bytes.Repeat([]byte("x"), test.fileBytes)); err != nil {
				t.Fatal(err)
			}
			if test.extra > 0 {
				extra, err := writer.CreateFormField("extra")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := extra.Write(bytes.Repeat([]byte("x"), test.extra)); err != nil {
					t.Fatal(err)
				}
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/documents", &body)
			req.Header.Set("Content-Type", writer.FormDataContentType())
			validationHandler().ServeHTTP(rec, req)
			if rec.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
			}
		})
	}
}

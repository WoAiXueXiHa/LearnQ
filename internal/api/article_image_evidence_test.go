package api

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/WoAiXueXiHa/LearnQ/internal/domain"
	"github.com/WoAiXueXiHa/LearnQ/internal/rag"
)

func TestArticleImageEvidenceSourceValidation(t *testing.T) {
	source := "# 图示\n\n![流程](https://example.com/a.png)\n\n![重复](https://example.com/a.png)\n"
	sum := sha256.Sum256([]byte(source))
	version := domain.DocumentIndex{ID: 2, DocumentID: 1, Content: source, ContentHash: hex.EncodeToString(sum[:])}
	parsed, err := rag.ParseMarkdown(source)
	if err != nil || len(parsed.ImageRefs) != 2 {
		t.Fatalf("image parse: %v, count %d", err, len(parsed.ImageRefs))
	}
	ref := parsed.ImageRefs[1]
	row := domain.ArticleImage{DocumentID: 1, IndexID: 2, OriginalURL: ref.URL, SyntaxKind: ref.Kind, StartByte: ref.StartByte, EndByte: ref.EndByte, StartLine: ref.StartLine, EndLine: ref.EndLine}
	if err := verifyArticleImage(version, row); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*domain.ArticleImage){
		func(r *domain.ArticleImage) { r.StartByte++ },
		func(r *domain.ArticleImage) { r.StartLine++ },
		func(r *domain.ArticleImage) { r.OriginalURL = "https://example.com/other.png" },
		func(r *domain.ArticleImage) { r.IndexID++ },
		func(r *domain.ArticleImage) { r.DocumentID++ },
	} {
		invalid := row
		change(&invalid)
		if verifyArticleImage(version, invalid) == nil {
			t.Fatal("accepted mismatched source occurrence")
		}
	}
	version.Content += "changed"
	if verifyArticleImage(version, row) == nil {
		t.Fatal("accepted altered source snapshot")
	}
}

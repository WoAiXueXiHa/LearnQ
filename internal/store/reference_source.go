package store

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/WoAiXueXiHa/LearnQ/internal/domain"
)

func validateReferenceSource(version domain.DocumentIndex, chunk domain.DocumentChunk) error {
	hash := func(text string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(text))) }
	if hash(version.Content) != version.ContentHash || hash(chunk.Content) != chunk.ContentHash {
		return invalidResult("reference source hash mismatch")
	}
	start, end := chunk.StartByte, chunk.EndByte
	if start < 0 || end <= start || end > len(version.Content) || !utf8.ValidString(version.Content[:start]) || !utf8.ValidString(version.Content[:end]) || version.Content[start:end] != chunk.Content {
		return invalidResult("reference source bytes mismatch")
	}
	if chunk.StartLine != strings.Count(version.Content[:start], "\n")+1 || chunk.EndLine != strings.Count(version.Content[:end-1], "\n")+1 {
		return invalidResult("reference source lines mismatch")
	}
	return nil
}

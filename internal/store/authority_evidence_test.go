package store

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/WoAiXueXiHa/LearnQ/internal/domain"
)

func TestAuthorityEvidenceValidation(t *testing.T) {
	hash := func(s string) string { sum := sha256.Sum256([]byte(s)); return hex.EncodeToString(sum[:]) }
	base := domain.AuthoritySnapshot{Content: "<p>Versioned source evidence.</p>", ExtractedText: "Versioned source evidence.", VersionLabel: "v1", ExpiresAt: time.Now().Add(time.Hour)}
	base.ContentHash, base.TextHash = hash(base.Content), hash(base.ExtractedText)
	for _, tc := range []struct {
		name    string
		mutate  func(*domain.AuthoritySnapshot)
		excerpt string
		valid   bool
	}{
		{"valid", nil, "source evidence", true},
		{"raw tampered", func(s *domain.AuthoritySnapshot) { s.Content += "changed" }, "source evidence", false},
		{"text tampered", func(s *domain.AuthoritySnapshot) { s.ExtractedText += "changed" }, "source evidence", false},
		{"expired", func(s *domain.AuthoritySnapshot) { s.ExpiresAt = time.Now().Add(-time.Second) }, "source evidence", false},
		{"unknown version", func(s *domain.AuthoritySnapshot) { s.VersionLabel = "  " }, "source evidence", false},
		{"invented excerpt", nil, "absent statement", false},
		{"empty excerpt", nil, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := base
			if tc.mutate != nil {
				tc.mutate(&snapshot)
			}
			if err := ValidateAuthorityEvidence(snapshot, tc.excerpt); (err == nil) != tc.valid {
				t.Fatalf("validation error = %v, want valid %v", err, tc.valid)
			}
		})
	}
}

func TestAuthorityRequestIdentity(t *testing.T) {
	base := authorityRequestKey(1, 2, "source excerpt", "v1 context")
	if base != authorityRequestKey(1, 2, "source excerpt", "v1 context") {
		t.Fatal("identical request changed identity")
	}
	seen := map[string]bool{base: true}
	for _, key := range []string{
		authorityRequestKey(2, 2, "source excerpt", "v1 context"),
		authorityRequestKey(1, 3, "source excerpt", "v1 context"),
		authorityRequestKey(1, 2, "other excerpt", "v1 context"),
		authorityRequestKey(1, 2, "source excerpt", "v2 context"),
		authorityRequestKey(1, 2, "a:b", "c"),
		authorityRequestKey(1, 2, "a", "b:c"),
	} {
		if seen[key] {
			t.Fatal("distinct evidence requests share identity")
		}
		seen[key] = true
	}
}

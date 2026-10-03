package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestImageEvidenceContentIntegrity(t *testing.T) {
	valid := `{"summary":"visible arrow","uncertainties":["label unclear"]}`
	hash := func(s string) string { sum := sha256.Sum256([]byte(s)); return hex.EncodeToString(sum[:]) }
	for _, tc := range []struct {
		name, content, digest, status string
		valid                         bool
	}{
		{"ready", valid, hash(valid), "ready", true},
		{"changed description", `{"summary":"invented arrow"}`, hash(valid), "ready", false},
		{"corrupt JSON with matching hash", "{", hash("{"), "ready", false},
		{"not indexed", valid, hash(valid), "pending", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := ImageEvidence{Content: tc.content, ContentHash: tc.digest, Status: tc.status}
			if err := e.ValidateContent(); (err == nil) != tc.valid {
				t.Fatalf("validation: %v, want valid %v", err, tc.valid)
			}
		})
	}
}

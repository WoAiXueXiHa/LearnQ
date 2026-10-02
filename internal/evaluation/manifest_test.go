package evaluation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Public frozen inputs must change together with their manifest. Private article
// excerpts and historical reports are deliberately not required in a checkout.
func TestFrozenManifestPublicInputs(t *testing.T) {
	root := filepath.Join("..", "..")
	body, err := os.ReadFile(filepath.Join(root, "data/eval/redis_persistence_manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]json.RawMessage
	if err := json.Unmarshal(body, &manifest); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"fixture", "current_chunker", "errata", "rag_regression"} {
		t.Run(name, func(t *testing.T) {
			var entry struct {
				Path   string `json:"path"`
				SHA256 string `json:"sha256"`
			}
			if err := json.Unmarshal(manifest[name], &entry); err != nil {
				t.Fatal(err)
			}
			if entry.Path == "" || len(entry.SHA256) != 64 {
				t.Fatal("missing frozen path or SHA-256")
			}
			input, err := os.ReadFile(filepath.Join(root, entry.Path))
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(input)
			if actual := hex.EncodeToString(sum[:]); actual != entry.SHA256 {
				t.Fatalf("%s changed: got %s, frozen %s; review input changes and update manifest", entry.Path, actual, entry.SHA256)
			}
		})
	}
}

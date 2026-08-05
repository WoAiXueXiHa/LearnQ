package report

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExportAtomicallyReplacesCopy(t *testing.T) {
	dir := t.TempDir()
	if err := Export(dir, 9, "first"); err != nil {
		t.Fatal(err)
	}
	if err := Export(dir, 9, "second"); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "9.md"))
	if err != nil || string(body) != "second" {
		t.Fatalf("body=%q err=%v", body, err)
	}
	temps, _ := filepath.Glob(filepath.Join(dir, ".report-*.tmp"))
	if len(temps) != 0 {
		t.Fatalf("temporary files remain: %v", temps)
	}
}

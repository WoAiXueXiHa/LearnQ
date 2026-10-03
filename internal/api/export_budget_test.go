package api

import (
	"bytes"
	"errors"
	"testing"
)

func TestExportBudgetAcrossEntries(t *testing.T) {
	budget := &exportBudget{remaining: 5}
	var first, second bytes.Buffer
	one, two := exportWriter{&first, budget}, exportWriter{&second, budget}
	if n, err := one.Write([]byte("abc")); n != 3 || err != nil {
		t.Fatalf("first write: %d %v", n, err)
	}
	if n, err := two.Write([]byte("def")); n != 0 || !errors.Is(err, errExportTooLarge) {
		t.Fatalf("oversize write: %d %v", n, err)
	}
	if second.Len() != 0 || budget.remaining != 2 {
		t.Fatal("rejected entry consumed bytes")
	}
	if n, err := two.Write([]byte("de")); n != 2 || err != nil {
		t.Fatalf("boundary write: %d %v", n, err)
	}
	if n, err := one.Write([]byte("x")); n != 0 || !errors.Is(err, errExportTooLarge) {
		t.Fatalf("exhausted write: %d %v", n, err)
	}
}

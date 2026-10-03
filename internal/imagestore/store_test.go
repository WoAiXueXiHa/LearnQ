package imagestore

import (
	"bytes"
	"testing"
)

func TestSaveReadDeleteAndRejectTraversal(t *testing.T) {
	store := New(t.TempDir())
	body := []byte("fake-png-body")
	name, err := store.Save(body, ".png")
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Read(name)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("read=%q err=%v", got, err)
	}
	if _, err := store.Read("../secret"); err == nil {
		t.Fatal("path traversal was accepted")
	}
	if err := store.Delete(name); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(name); err == nil {
		t.Fatal("deleted image remained readable")
	}
}

func TestSaveRejectsUnsupportedOrOversizedImage(t *testing.T) {
	store := New(t.TempDir())
	if _, err := store.Save([]byte("x"), ".svg"); err == nil {
		t.Fatal("unsupported svg was accepted")
	}
	if _, err := store.Save(make([]byte, MaxBytes+1), ".png"); err == nil {
		t.Fatal("oversized image was accepted")
	}
}

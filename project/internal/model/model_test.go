package model

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestFakeFailureInjection(t *testing.T) {
	if _, err := (Fake{Failure: "temporary"}).Generate(context.Background(), ChatRequest{}); err == nil {
		t.Fatal("temporary error not injected")
	}
	response, err := (Fake{Failure: "invalid_json"}).Generate(context.Background(), ChatRequest{})
	if err != nil || response.Content != "{" {
		t.Fatalf("response=%#v err=%v", response, err)
	}
}

func TestFakeHonorsContextTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	_, err := (Fake{Delay: time.Second}).Generate(ctx, ChatRequest{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
}

func TestMarkdownRequiresStructuredFields(t *testing.T) {
	if _, err := MarkdownFromJSON(`{"title":"x"}`); err == nil {
		t.Fatal("missing required fields accepted")
	}
	if _, err := MarkdownFromJSON(`{"title":"x","summary":"y","sections":[{"heading":"h","content":"c"}]}`); err != nil {
		t.Fatal(err)
	}
}

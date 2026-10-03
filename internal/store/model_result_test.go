package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/WoAiXueXiHa/LearnQ/internal/model"
)

func TestCachedPracticeResponseIntegrity(t *testing.T) {
	want := model.ChatResponse{Content: `{"questions":[]}`, Model: "fake", InputTokens: 12, OutputTokens: 8}
	body, _ := json.Marshal(want)
	sum := sha256.Sum256(body)
	row := PracticeModelResult{InputHash: "request-v1", ResponseJSON: string(body), ResponseHash: hex.EncodeToString(sum[:])}
	got, err := row.decode("request-v1")
	if err != nil || got != want {
		t.Fatalf("response round trip: %#v %v", got, err)
	}
	if _, err := row.decode("different-input"); !IsResultValidationError(err) {
		t.Fatal("different request reused cached response")
	}
	row.ResponseJSON = `{"Content":"tampered"}`
	if _, err := row.decode("request-v1"); !IsResultValidationError(err) {
		t.Fatal("tampered response reused")
	}
	row.ResponseJSON = "{"
	sum = sha256.Sum256([]byte(row.ResponseJSON))
	row.ResponseHash = hex.EncodeToString(sum[:])
	if _, err := row.decode("request-v1"); !IsResultValidationError(err) {
		t.Fatal("invalid response envelope reused")
	}
}

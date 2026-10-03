package worker

import (
	"errors"
	"fmt"
	"testing"

	"github.com/WoAiXueXiHa/LearnQ/internal/model"
	"github.com/WoAiXueXiHa/LearnQ/internal/store"
)

func TestPracticeResultFailureClassification(t *testing.T) {
	rejected := fmt.Errorf("transaction: %w", &store.ResultValidationError{Reason: "unprovided citation"})
	var permanent *model.PermanentError
	if err := classifyPracticeResultError(rejected); !errors.As(err, &permanent) {
		t.Fatalf("invalid result must stop automatic calls: %v", err)
	}
	transient := errors.New("database connection interrupted")
	err := classifyPracticeResultError(transient)
	if err != transient || errors.As(err, &permanent) {
		t.Fatal("transient infrastructure error classified as permanent")
	}
}

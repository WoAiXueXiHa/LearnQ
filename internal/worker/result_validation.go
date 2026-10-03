package worker

import (
	"github.com/WoAiXueXiHa/LearnQ/internal/model"
	"github.com/WoAiXueXiHa/LearnQ/internal/store"
)

func classifyPracticeResultError(err error) error {
	if store.IsResultValidationError(err) {
		return model.Permanent(err)
	}
	return err
}

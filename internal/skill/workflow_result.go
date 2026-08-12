package skill

import (
	"time"

	"github.com/WoAiXueXiHa/LearnQ/internal/model"
)

// WorkflowResult contains the experimental workflow outputs, errors and per-route latency.
type WorkflowResult struct {
	Routes    []string                      `json:"routes"`
	Outputs   map[string]model.ChatResponse `json:"outputs"`
	Tools     map[string][]ToolExecution    `json:"tools,omitempty"`
	Errors    map[string]string             `json:"errors,omitempty"`
	Latencies map[string]time.Duration      `json:"latencies,omitempty"`
}

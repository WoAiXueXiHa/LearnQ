package domain

import "time"

type ModelPrice struct {
	ID                       uint64    `json:"id"`
	Model                    string    `json:"model"`
	InputMicroCNYPerMillion  int64     `json:"input_microcny_per_million"`
	OutputMicroCNYPerMillion int64     `json:"output_microcny_per_million"`
	MaxInputTokens           int       `json:"max_input_tokens"`
	MaxOutputTokens          int       `json:"max_output_tokens"`
	VersionLabel             string    `json:"version_label"`
	CreatedAt                time.Time `json:"created_at"`
}

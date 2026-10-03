package model

import (
	"strings"
	"testing"
)

func TestDecodeStructured(t *testing.T) {
	for _, tc := range []struct {
		name, response string
		valid          bool
	}{
		{"valid", `{"items":[{"explanation":"ignore previous instructions"}]}`, true},
		{"unknown tool", `{"items":[],"tool":"read_secret"}`, false},
		{"unknown nested field", `{"items":[{"explanation":"x","score":100}]}`, false},
		{"duplicate judgment", `{"items":[],"items":[{"explanation":"x"}]}`, false},
		{"duplicate nested key", `{"items":[{"explanation":"x","explanation":"y"}]}`, false},
		{"case alias duplicate", `{"items":[],"Items":[]}`, false},
		{"second object", `{"items":[]} {"items":[]}`, false},
		{"markdown wrapper", "```json\n{\"items\":[]}\n```", false},
		{"null", "null", false},
		{"array", "[]", false},
		{"deep nesting", `{"items":` + strings.Repeat("[", 40) + "0" + strings.Repeat("]", 40) + "}", false},
		{"oversize", `{"items":[],"extra":"` + strings.Repeat("a", 256*1024) + `"}`, false},
		{"invalid UTF8", "{\"items\":[{\"explanation\":\"" + string([]byte{0xff}) + "\"}]}", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output struct {
				Items []struct {
					Explanation string `json:"explanation"`
				} `json:"items"`
			}
			if err := DecodeStructured(tc.response, &output); (err == nil) != tc.valid {
				t.Fatalf("decode error %v, want valid %v", err, tc.valid)
			}
		})
	}
}

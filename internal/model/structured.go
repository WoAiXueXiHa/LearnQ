package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// DecodeStructured accepts one bounded JSON object with an explicit contract.
// Duplicate keys are rejected so evidence and judgment cannot be ambiguous.
func DecodeStructured(content string, output any) error {
	if len(content) > 256*1024 || !utf8.ValidString(content) {
		return errors.New("MODEL_OUTPUT_INVALID: response too large or invalid UTF-8")
	}
	trimmed := strings.TrimSpace(content)
	if !strings.HasPrefix(trimmed, "{") {
		return errors.New("MODEL_OUTPUT_INVALID: expected JSON object")
	}
	scan := json.NewDecoder(strings.NewReader(content))
	if err := scanJSONValue(scan, 0); err != nil {
		return fmt.Errorf("MODEL_OUTPUT_INVALID: %w", err)
	}
	if _, err := scan.Token(); err != io.EOF {
		return errors.New("MODEL_OUTPUT_INVALID: trailing content")
	}
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return fmt.Errorf("MODEL_OUTPUT_INVALID: %w", err)
	}
	return nil
}

func scanJSONValue(decoder *json.Decoder, depth int) error {
	if depth > 32 {
		return errors.New("MODEL_OUTPUT_INVALID: JSON nesting exceeds limit")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, container := token.(json.Delim)
	if !container {
		return nil
	}
	switch delimiter {
	case '{':
		keys := map[string]bool{}
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			name, valid := key.(string)
			name = strings.ToLower(name)
			if !valid || keys[name] {
				return errors.New("MODEL_OUTPUT_INVALID: duplicate or invalid object key")
			}
			keys[name] = true
			if err := scanJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
	default:
		return errors.New("MODEL_OUTPUT_INVALID: unexpected delimiter")
	}
	_, err = decoder.Token()
	return err
}

package worker

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/WoAiXueXiHa/LearnQ/internal/domain"
)

// Full chunks are sent once, with no second copy of the article or DB metadata.
// This conservative byte limit is an input bound, not a token/price estimate.
const maxQuestionInputBytes = 96 * 1024

func buildQuestionInput(chunks []domain.DocumentChunk, images []map[string]any) ([]byte, error) {
	type evidence struct {
		ID          string          `json:"id"`
		Content     string          `json:"content"`
		HeadingPath json.RawMessage `json:"heading_path"`
		StartLine   int             `json:"start_line"`
		EndLine     int             `json:"end_line"`
	}
	rows := []evidence{}
	sections := []string{}
	seen := map[string]bool{}
	for _, chunk := range chunks {
		path := chunk.HeadingPathJSON
		if path == "" {
			path = "[]"
		}
		if !json.Valid([]byte(path)) {
			return nil, errors.New("invalid stored heading path")
		}
		rows = append(rows, evidence{chunk.ID, chunk.Content, json.RawMessage(path), chunk.StartLine, chunk.EndLine})
		if !seen[path] {
			sections = append(sections, path)
			seen[path] = true
		}
	}
	input, err := json.Marshal(map[string]any{"sections": sections, "chunks": rows, "images": images})
	if err != nil {
		return nil, err
	}
	if len(input) > maxQuestionInputBytes {
		return nil, fmt.Errorf("article exceeds five-question input limit (%d bytes); split into coherent articles; no content was silently truncated", maxQuestionInputBytes)
	}
	return input, nil
}

// Coverage verifies supplied identities and chapter/image presence only. It
// cannot prove that a question meaningfully examines the cited facts.
func validateQuestionCoverage(chunks []domain.DocumentChunk, images []map[string]any, questions []domain.QuestionDraft) error {
	byID := map[string]string{}
	sections, covered := map[string]bool{}, map[string]bool{}
	headingOnly := map[string]bool{}
	for _, c := range chunks {
		byID[c.ID] = c.HeadingPathJSON
		if !sections[c.HeadingPathJSON] {
			headingOnly[c.HeadingPathJSON] = true
		}
		sections[c.HeadingPathJSON] = true
		headingOnly[c.HeadingPathJSON] = headingOnly[c.HeadingPathJSON] && isHeadingOnlyChunk(c.Content)
	}
	allowedImages, coveredImages := map[uint64]bool{}, map[uint64]bool{}
	for _, image := range images {
		id, ok := image["image_ref_id"].(uint64)
		if !ok {
			return errors.New("invalid image input identity")
		}
		allowedImages[id] = true
	}
	if len(questions) != 5 {
		return errors.New("expected exactly five questions")
	}
	for _, q := range questions {
		if len(q.ReferenceItems) < 2 || len(q.ReferenceItems) > 4 {
			return errors.New("generated questions need two to four structured reference items")
		}
		for _, id := range q.ChunkIDs {
			section, ok := byID[id]
			if !ok {
				return errors.New("unknown generated chunk ID")
			}
			covered[section] = true
		}
		for _, id := range q.ImageRefIDs {
			if !allowedImages[id] {
				return errors.New("unknown generated image ID")
			}
			coveredImages[id] = true
		}
	}
	for section := range sections {
		if !covered[section] && !(headingOnly[section] && hasCoveredChildSection(section, covered)) {
			return errors.New("question set omits an article section; revise generation rather than dropping source content")
		}
	}
	for id := range allowedImages {
		if !coveredImages[id] {
			return errors.New("question set omits an image evidence source")
		}
	}
	return nil
}

// A heading-only parent names the topic; citing its substantive child covers
// that heading. Parents containing introductory facts still require citations.
func isHeadingOnlyChunk(content string) bool {
	found := false
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		n := len(line) - len(strings.TrimLeft(line, "#"))
		if n < 1 || n > 6 || len(line) <= n || (line[n] != ' ' && line[n] != '\t') {
			return false
		}
		found = true
	}
	return found
}
func hasCoveredChildSection(parent string, covered map[string]bool) bool {
	var ancestor []string
	if json.Unmarshal([]byte(parent), &ancestor) != nil || len(ancestor) == 0 {
		return false
	}
	for section := range covered {
		var path []string
		if json.Unmarshal([]byte(section), &path) != nil || len(path) <= len(ancestor) {
			continue
		}
		match := true
		for i, heading := range ancestor {
			if path[i] != heading {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

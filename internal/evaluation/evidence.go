package evaluation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// Evidence intervals locate persisted chunk bytes in the frozen original article.
// Coverage unions only retrieved intervals and requires an entire accepted span.
type EvidenceInterval struct {
	ChunkID string `json:"chunk_id"`
	Start   int    `json:"start_byte"`
	End     int    `json:"end_byte"`
}
type EvidenceSpan struct {
	Text      string             `json:"text"`
	Start     int                `json:"start_byte"`
	End       int                `json:"end_byte"`
	Intervals []EvidenceInterval `json:"intervals"`
}
type EvidencePoint struct {
	Spans        []EvidenceSpan `json:"spans,omitempty"`
	Point        string         `json:"point"`
	Lines        [][]int        `json:"lines"`
	ChunkIDs     []string       `json:"chunk_ids,omitempty"`
	Alternatives [][]string     `json:"alternatives,omitempty"`
}
type EvidenceChunk struct {
	ID, Content        string
	StartLine, EndLine int
	StartByte          *int
}
type EvidenceLoader func(context.Context, uint64) (string, []EvidenceChunk, error)
type EvidenceResult struct {
	DocumentID    uint64   `json:"document_id"`
	ArticleSHA256 string   `json:"article_sha256"`
	Lines         [][]int  `json:"lines"`
	Excerpts      []string `json:"excerpts"`
	Point         string   `json:"point"`
	Covered       bool     `json:"covered"`
	ChunkIDs      []string `json:"chunk_ids"`
}

func ResolveEvidencePoints(ctx context.Context, cases []Case, loader EvidenceLoader) ([]Case, error) {
	type loaded struct {
		source string
		chunks []EvidenceChunk
	}
	cache := map[uint64]loaded{}
	out := append([]Case(nil), cases...)
	for i := range out {
		item := &out[i]
		if len(item.EvidencePoints) == 0 {
			continue
		}
		if loader == nil {
			return nil, fmt.Errorf("evidence loader is required")
		}
		entry, exists := cache[item.DocumentID]
		if !exists {
			source, chunks, err := loader(ctx, item.DocumentID)
			if err != nil {
				return nil, fmt.Errorf("case %s: %w", item.ID, err)
			}
			entry = loaded{source, chunks}
			cache[item.DocumentID] = entry
		}
		source, chunks := entry.source, entry.chunks
		sum := sha256.Sum256([]byte(source))
		if hex.EncodeToString(sum[:]) != item.ArticleSHA256 {
			return nil, fmt.Errorf("case %s article hash mismatch", item.ID)
		}
		lines := strings.Split(source, "\n")
		offsets := make([]int, len(lines)+1)
		for j, line := range lines {
			offsets[j+1] = offsets[j] + len(line) + 1
		}
		item.EvidencePoints = append([]EvidencePoint(nil), item.EvidencePoints...)
		judgments := map[string]bool{}
		for j := range item.EvidencePoints {
			p := &item.EvidencePoints[j]
			p.ChunkIDs = nil
			p.Alternatives = nil
			p.Spans = nil
			for _, span := range p.Lines {
				if len(span) != 2 || span[0] < 1 || span[1] < span[0] || span[1] > len(lines) {
					return nil, fmt.Errorf("case %s invalid evidence span", item.ID)
				}
				start, end := offsets[span[0]-1], offsets[span[1]]-1
				if strings.TrimSpace(source[start:end]) == "" {
					return nil, fmt.Errorf("case %s empty evidence span", item.ID)
				}
				type interval struct {
					start, end int
					id         string
				}
				var ranges []interval
				for _, chunk := range chunks {
					if chunk.ID == "" || chunk.Content == "" {
						continue
					}
					if chunk.StartByte != nil {
						n := *chunk.StartByte
						if n < 0 || n+len(chunk.Content) > len(source) || source[n:n+len(chunk.Content)] != chunk.Content || sort.Search(len(offsets), func(i int) bool { return offsets[i] > n }) != chunk.StartLine {
							return nil, fmt.Errorf("case %s invalid chunk offset", item.ID)
						}
						ranges = append(ranges, interval{n, n + len(chunk.Content), chunk.ID})
						continue
					}
					var matches []interval
					for at := 0; at < len(source); {
						n := strings.Index(source[at:], chunk.Content)
						if n < 0 {
							break
						}
						n += at
						if sort.Search(len(offsets), func(i int) bool { return offsets[i] > n }) == chunk.StartLine {
							matches = append(matches, interval{n, n + len(chunk.Content), chunk.ID})
						}
						at = n + 1
					}
					if len(matches) > 1 {
						return nil, fmt.Errorf("case %s ambiguous chunk position %s", item.ID, chunk.ID)
					}
					ranges = append(ranges, matches...)

				}
				mappedSpan := EvidenceSpan{Start: start, End: end, Text: source[start:end]}
				for _, r := range ranges {
					if r.start < end && r.end > start {
						mappedSpan.Intervals = append(mappedSpan.Intervals, EvidenceInterval{ChunkID: r.id, Start: r.start, End: r.end})
					}
				}
				p.Spans = append(p.Spans, mappedSpan)
				// Preserve single-chunk alternatives in overlap windows.
				contained := false
				for _, r := range ranges {
					if r.start <= start && r.end >= end {
						p.Alternatives = append(p.Alternatives, []string{r.id})
						contained = true
					}
				}
				if !contained {
					sort.Slice(ranges, func(a, b int) bool { return ranges[a].end > ranges[b].end })
					pos := start
					var ids []string
					for pos < end {
						best := pos
						id := ""
						for _, r := range ranges {
							if r.start <= pos && r.end > best {
								best = r.end
								id = r.id
							}
						}
						if id == "" {
							break
						}
						ids = append(ids, id)
						pos = best
					}
					if pos >= end {
						p.Alternatives = append(p.Alternatives, ids)
					}
				}
			}
			if len(p.Alternatives) == 0 {
				return nil, fmt.Errorf("case %s evidence point %q has no complete indexed span", item.ID, p.Point)
			}
			seen := map[string]bool{}
			for _, span := range p.Spans {
				for _, r := range span.Intervals {
					judgments[r.ChunkID] = true
					if !seen[r.ChunkID] {
						p.ChunkIDs = append(p.ChunkIDs, r.ChunkID)
						seen[r.ChunkID] = true
					}
				}
			}

		}
		item.RelevantChunks = nil
		var ids []string
		for id := range judgments {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			item.RelevantChunks = append(item.RelevantChunks, Judgment{ChunkID: id, Relevance: 3})
		}
	}
	return out, nil
}

func evidenceCovered(p EvidencePoint, retrieved map[string]bool) bool {
	if len(p.Spans) > 0 {
		for _, span := range p.Spans {
			pos := span.Start
			for pos < span.End {
				best := pos
				for _, r := range span.Intervals {
					if retrieved[r.ChunkID] && r.Start <= pos && r.End > best {
						best = r.End
					}
				}
				if best == pos {
					break
				}
				pos = best
			}
			if pos >= span.End {
				return true
			}
		}
		return false
	}
	for _, group := range p.Alternatives {
		complete := len(group) > 0
		for _, id := range group {
			complete = complete && retrieved[id]
		}
		if complete {
			return true
		}
	}
	return false
}

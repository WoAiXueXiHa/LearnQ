package api

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/WoAiXueXiHa/LearnQ/internal/domain"
	"github.com/WoAiXueXiHa/LearnQ/internal/rag"
	"github.com/gin-gonic/gin"
)

func validateSourceHash(source, hash string) error {
	sum := sha256.Sum256([]byte(source))
	if hex.EncodeToString(sum[:]) != hash {
		return errors.New("source snapshot hash mismatch")
	}
	return nil
}

// evidenceChunkPosition verifies bytes and lines, even for repeated text.
// The historical chunker is used only for explicitly unversioned legacy rows.
func evidenceChunkPosition(source string, chunk domain.DocumentChunk) (int, int, error) {
	start, end := chunk.StartByte, chunk.EndByte
	if chunk.IndexID == 0 {
		legacy := rag.ChunkText(source, 800, 120)
		if chunk.ChunkIndex < 0 || chunk.ChunkIndex >= len(legacy) || legacy[chunk.ChunkIndex].Content != chunk.Content {
			return 0, 0, errors.New("legacy chunk cannot be located with historical chunk-v1 contract")
		}
		runes := []rune(source)
		runeStart := chunk.ChunkIndex * (800 - 120)
		start = len(string(runes[:runeStart]))
		end = start + len(chunk.Content)
	}
	if start < 0 || end <= start || end > len(source) || !utf8.ValidString(source[:start]) || !utf8.ValidString(source[:end]) || source[start:end] != chunk.Content {
		return 0, 0, errors.New("persisted chunk byte interval does not match source snapshot")
	}
	if chunk.StartLine != strings.Count(source[:start], "\n")+1 || chunk.EndLine != strings.Count(source[:end-1], "\n")+1 {
		return 0, 0, errors.New("persisted chunk lines do not match source snapshot")
	}
	if err := validateSourceHash(chunk.Content, chunk.ContentHash); err != nil {
		return 0, 0, errors.New("persisted chunk hash mismatch")
	}
	return start, end, nil
}

// documentEvidence reads a historical citation by immutable chunk identity.
// Deleted documents return 404; corrupt or unprovable legacy evidence returns 410.
func (s *Server) documentEvidence(c *gin.Context) {
	id, valid := parseID(c)
	if !valid {
		return
	}
	indexID, err := strconv.ParseUint(c.Param("index_id"), 10, 64)
	if err != nil {
		fail(c, 400, "VALIDATION_FAILED", "invalid index id", nil)
		return
	}
	var document domain.Document
	if lookupFailed(c, s.store.DB.WithContext(c.Request.Context()).Where("id=? AND status<>'deleting'", id).First(&document).Error, "could not load document") {
		return
	}
	source, hash := document.Content, document.ContentHash
	if indexID != 0 {
		var version domain.DocumentIndex
		if lookupFailed(c, s.store.DB.WithContext(c.Request.Context()).Where("id=? AND document_id=?", indexID, id).First(&version).Error, "could not load index snapshot") {
			return
		}
		if version.Status != "active" && version.Status != "retired" {
			fail(c, 409, "EVIDENCE_NOT_READY", "index is still building", nil)
			return
		}
		source, hash = version.Content, version.ContentHash
	}
	var chunk domain.DocumentChunk
	if lookupFailed(c, s.store.DB.WithContext(c.Request.Context()).Where("id=? AND document_id=? AND index_id=?", c.Param("chunk_id"), id, indexID).First(&chunk).Error, "could not load evidence chunk") {
		return
	}
	if err := validateSourceHash(source, hash); err != nil {
		fail(c, 410, "EVIDENCE_INVALID", err.Error(), nil)
		return
	}
	start, end, err := evidenceChunkPosition(source, chunk)
	if err != nil {
		fail(c, 410, "EVIDENCE_INVALID", err.Error(), nil)
		return
	}
	fragment, err := renderEvidenceMarkdown(source[start:end])
	if err != nil {
		fail(c, 500, "INTERNAL_ERROR", "could not render evidence", nil)
		return
	}
	full, err := renderEvidenceMarkdown(source)
	if err != nil {
		fail(c, 500, "INTERNAL_ERROR", "could not render source", nil)
		return
	}
	ok(c, 200, gin.H{"document_name": document.Filename, "rendered_html": fragment, "source_html": full, "document_id": id, "index_id": indexID, "article_sha256": hash,
		"active": document.ActiveIndexID == indexID, "chunk": chunk,
		"start_byte": start, "end_byte": end, "source": source})
}

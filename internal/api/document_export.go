package api

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/WoAiXueXiHa/LearnQ/internal/domain"
	"github.com/gin-gonic/gin"
)

// Export uses a private temporary file so errors never return a partial archive.
// Runtime secrets and provider credentials are outside the explicit table list.
func (s *Server) exportDocument(c *gin.Context) {
	id, valid := parseID(c)
	if !valid {
		return
	}
	select {
	case exportSlots <- struct{}{}:
		defer func() { <-exportSlots }()
	default:
		c.Header("Retry-After", "5")
		fail(c, 429, "EXPORT_BUSY", "another export is running; retry shortly", nil)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Minute)
	defer cancel()
	tx := s.store.DB.WithContext(ctx).Begin(&sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if tx.Error != nil {
		fail(c, 500, "EXPORT_FAILED", "could not open consistent export snapshot", nil)
		return
	}
	defer tx.Rollback()
	var document domain.Document
	if lookupFailed(c, tx.Where("id=? AND status<>'deleting'", id).First(&document).Error, "could not load article") {
		return
	}
	file, err := os.CreateTemp("", "learnq-export-*.zip")
	if err != nil {
		fail(c, 500, "EXPORT_FAILED", "could not prepare export", nil)
		return
	}
	defer os.Remove(file.Name())
	defer file.Close()
	const maxExportBytes = 256 << 20
	compressed := &exportBudget{remaining: maxExportBytes}
	raw := &exportBudget{remaining: maxExportBytes}
	archive := zip.NewWriter(exportWriter{writer: file, budget: compressed})
	createEntry := func(name string) (io.Writer, error) {
		output, err := archive.Create(name)
		if err != nil {
			return nil, err
		}
		return exportWriter{writer: output, budget: raw}, nil
	}
	buildErr := func() error {
		var submittedSetIDs []uint64
		if err := tx.Table("practice_attempts").Where("submitted_at IS NOT NULL AND question_set_id IN (SELECT id FROM question_sets WHERE document_id=?)", id).Distinct("question_set_id").Pluck("question_set_id", &submittedSetIDs).Error; err != nil {
			return err
		}
		submitted := map[uint64]bool{}
		for _, setID := range submittedSetIDs {
			submitted[setID] = true
		}
		original, err := createEntry("article.md")
		if err != nil {
			return err
		}
		if _, err = io.WriteString(original, document.Content); err != nil {
			return err
		}
		setScope := "SELECT id FROM question_sets WHERE document_id=?"
		attemptScope := "SELECT id FROM practice_attempts WHERE question_set_id IN (" + setScope + ")"
		feedbackScope := "SELECT id FROM answer_feedback WHERE practice_attempt_id IN (" + attemptScope + ")"
		claimScope := "SELECT id FROM authority_claims WHERE answer_feedback_id IN (" + feedbackScope + ")"
		checkScope := "SELECT id FROM authority_checks WHERE authority_claim_id IN (" + claimScope + ")"
		queries := []struct{ name, sql string }{
			{"document", "SELECT * FROM documents WHERE id=?"},
			{"indexes", "SELECT * FROM document_indexes WHERE document_id=?"},
			{"chunks", "SELECT * FROM document_chunks WHERE document_id=?"},
			{"image_references", "SELECT * FROM article_images WHERE document_id=?"},
			{"image_evidence", "SELECT * FROM image_evidence WHERE document_id=?"},
			{"images", "SELECT * FROM images WHERE id IN (SELECT image_id FROM article_images WHERE document_id=?)"},
			{"question_sets", "SELECT * FROM question_sets WHERE document_id=?"},
			{"questions", "SELECT * FROM practice_questions WHERE question_set_id IN (" + setScope + ")"},
			{"question_edits", "SELECT * FROM question_set_edits WHERE question_set_id IN (" + setScope + ")"},
			{"attempts", "SELECT * FROM practice_attempts WHERE question_set_id IN (" + setScope + ")"},
			{"feedback", "SELECT * FROM answer_feedback WHERE practice_attempt_id IN (" + attemptScope + ")"},
			{"corrections", "SELECT * FROM feedback_corrections WHERE answer_feedback_id IN (" + feedbackScope + ")"},
			{"reviews", "SELECT * FROM practice_reviews WHERE practice_attempt_id IN (" + attemptScope + ")"},
			{"claims", "SELECT * FROM authority_claims WHERE answer_feedback_id IN (" + feedbackScope + ")"},
			{"image_description_corrections", "SELECT * FROM image_description_corrections WHERE document_id=?"},
			{"authority_checks", "SELECT * FROM authority_checks WHERE authority_claim_id IN (" + claimScope + ")"},
			{"authority_reviews", "SELECT * FROM authority_check_reviews WHERE authority_check_id IN (" + checkScope + ")"},
			{"authority_snapshots", "SELECT * FROM authority_snapshots WHERE id IN (SELECT authority_snapshot_id FROM authority_checks WHERE authority_claim_id IN (" + claimScope + "))"},
		}
		for _, query := range queries {
			output, err := createEntry(query.name + ".jsonl")
			if err != nil {
				return err
			}
			rows, err := tx.Raw(query.sql, id).Rows()
			if err != nil {
				return err
			}
			columns, err := rows.Columns()
			if err != nil {
				rows.Close()
				return err
			}
			encoder := json.NewEncoder(output)
			for rows.Next() {
				values := make([]any, len(columns))
				pointers := make([]any, len(columns))
				for j := range values {
					pointers[j] = &values[j]
				}
				if err := rows.Scan(pointers...); err != nil {
					rows.Close()
					return err
				}
				item := map[string]any{}
				for j, column := range columns {
					value := values[j]
					if raw, ok := value.([]byte); ok {
						value = string(raw)
					}
					item[column] = value
				}
				if query.name == "questions" || query.name == "question_sets" || query.name == "question_edits" {
					setColumn := "question_set_id"
					if query.name == "question_sets" {
						setColumn = "id"
					}
					setID, err := strconv.ParseUint(fmt.Sprint(item[setColumn]), 10, 64)
					if err != nil {
						rows.Close()
						return err
					}
					if !submitted[setID] {
						switch query.name {
						case "questions":
							item["reference_points_json"] = "[]"
							item["reference_items_json"] = "null"
						case "question_sets":
							item["original_json"] = "{}"
						case "question_edits":
							var drafts []domain.QuestionDraft
							if err := json.Unmarshal([]byte(fmt.Sprint(item["questions_json"])), &drafts); err != nil {
								rows.Close()
								return err
							}
							for j := range drafts {
								drafts[j].ReferencePoints = nil
								drafts[j].ReferenceItems = nil
							}
							body, err := json.Marshal(drafts)
							if err != nil {
								rows.Close()
								return err
							}
							item["questions_json"] = string(body)
						}
					}
				}
				if err := encoder.Encode(item); err != nil {
					rows.Close()
					return err
				}
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return err
			}
			rows.Close()
		}
		var images []domain.Image
		if err := tx.Where("id IN (SELECT image_id FROM article_images WHERE document_id=?)", id).Find(&images).Error; err != nil {
			return err
		}
		for _, image := range images {
			if s.imageStore == nil {
				return fmt.Errorf("image storage unavailable")
			}
			input, err := s.imageStore.Open(image.StoragePath)
			if err != nil {
				return err
			}
			output, err := createEntry(fmt.Sprintf("images/%d.original", image.ID))
			if err != nil {
				input.Close()
				return err
			}
			hash := sha256.New()
			_, err = io.Copy(io.MultiWriter(output, hash), input)
			input.Close()
			if err != nil {
				return err
			}
			if hex.EncodeToString(hash.Sum(nil)) != image.ContentHash {
				return fmt.Errorf("image snapshot hash mismatch")
			}
		}
		manifest, err := createEntry("manifest.json")
		if err != nil {
			return err
		}
		return json.NewEncoder(manifest).Encode(gin.H{"format": "learnq-article-export-v1", "document_id": id, "article_sha256": document.ContentHash, "complete": true, "database_consistency": "repeatable_read_snapshot", "max_export_bytes": maxExportBytes, "restore_compatible": false, "private_reference_policy": "withheld_for_question_sets_without_submitted_attempts"})
	}()
	closeErr := archive.Close()
	if errors.Is(buildErr, errExportTooLarge) || errors.Is(closeErr, errExportTooLarge) {
		fail(c, 413, "EXPORT_TOO_LARGE", "article export exceeds 256 MiB; use full backup for larger data", nil)
		return
	}
	if buildErr != nil || closeErr != nil {
		fail(c, 500, "EXPORT_FAILED", "could not build a complete article export", nil)
		return
	}
	if err := tx.Commit().Error; err != nil {
		fail(c, 500, "EXPORT_FAILED", "could not finish consistent export snapshot", nil)
		return
	}
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=learnq-article-%d.zip", id))
	c.Header("Cache-Control", "no-store")
	c.File(file.Name())
}

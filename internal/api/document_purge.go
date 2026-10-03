package api

import (
	"fmt"
	"net/http"

	"github.com/WoAiXueXiHa/LearnQ/internal/documentcleanup"
	"github.com/WoAiXueXiHa/LearnQ/internal/store"
	"github.com/gin-gonic/gin"
)

func (s *Server) documentDeletionPlan(c *gin.Context) {
	id, valid := parseID(c)
	if !valid {
		return
	}
	plan, hash, err := s.store.DocumentDeletionPlan(c.Request.Context(), id)
	if err != nil {
		fail(c, 404, "NOT_FOUND", "could not inspect article deletion impact", nil)
		return
	}
	ok(c, 200, gin.H{"plan": plan, "plan_hash": hash, "required_confirmation": fmt.Sprintf("DELETE %d", id)})
}

func (s *Server) purgeDocument(c *gin.Context) {
	id, valid := parseID(c)
	if !valid {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 2000)
	var input struct {
		PlanHash     string `json:"plan_hash"`
		Confirmation string `json:"confirmation"`
	}
	if c.ShouldBindJSON(&input) != nil || len(input.PlanHash) != 64 || input.Confirmation != fmt.Sprintf("DELETE %d", id) {
		fail(c, 422, "DELETION_CONFIRMATION_REQUIRED", "review impact and enter the exact deletion confirmation", nil)
		return
	}
	job, err := s.store.BeginDocumentPurge(c.Request.Context(), id, input.PlanHash)
	if err != nil {
		fail(c, 409, "DELETION_PLAN_CHANGED", err.Error(), nil)
		return
	}
	s.continueDocumentPurge(c, job)
}

func (s *Server) continueDocumentPurge(c *gin.Context, job store.DocumentDeletion) {
	updated, err := (&documentcleanup.Processor{Store: s.store, Images: s.imageStore, Vectors: s.vectors}).Process(c.Request.Context(), job)
	if err != nil {
		fail(c, 503, "DELETION_PENDING", err.Error(), gin.H{"deletion": updated})
		return
	}
	status := 202
	if updated.Status == "complete" {
		status = 200
	}
	ok(c, status, gin.H{"deletion": updated, "backups_erased": false})
}

func (s *Server) getDocumentDeletion(c *gin.Context) {
	id, valid := parseID(c)
	if !valid {
		return
	}
	var job store.DocumentDeletion
	if lookupFailed(c, s.store.DB.WithContext(c.Request.Context()).Where("document_id=?", id).First(&job).Error, "could not load deletion record") {
		return
	}
	ok(c, 200, job)
}

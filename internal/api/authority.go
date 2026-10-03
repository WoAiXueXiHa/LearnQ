package api

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/WoAiXueXiHa/LearnQ/internal/authority"
	"github.com/WoAiXueXiHa/LearnQ/internal/domain"
	"github.com/gin-gonic/gin"
)

func WithAuthoritySnapshotTTL(ttl time.Duration) Option {
	return func(s *Server) { s.authoritySnapshotTTL = ttl }
}

func (s *Server) registerAuthority(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 10000)
	var input struct {
		Topic string `json:"topic"`
		URL   string `json:"url"`
	}
	if c.ShouldBindJSON(&input) != nil || strings.TrimSpace(input.Topic) == "" || len(input.Topic) > 255 || len(input.URL) > 8000 {
		fail(c, 422, "VALIDATION_FAILED", "invalid authority registration", nil)
		return
	}
	u, err := authority.ValidateURL(input.URL)
	if err != nil {
		fail(c, 422, "VALIDATION_FAILED", err.Error(), nil)
		return
	}
	row := domain.AuthoritySource{Topic: input.Topic, URL: u.String(), Hostname: strings.ToLower(u.Hostname()), Status: "registered", CreatedAt: time.Now().UTC()}
	if err := s.store.DB.WithContext(c.Request.Context()).Create(&row).Error; err != nil {
		fail(c, 500, "INTERNAL_ERROR", "could not register authority source", nil)
		return
	}
	ok(c, 201, row)
}

func (s *Server) listAuthorities(c *gin.Context) {
	rows := []domain.AuthoritySource{}
	if err := s.store.DB.WithContext(c.Request.Context()).Order("id DESC").Limit(100).Find(&rows).Error; err != nil {
		fail(c, 500, "INTERNAL_ERROR", "could not load authority sources", nil)
		return
	}
	ok(c, 200, rows)
}

func (s *Server) captureAuthority(c *gin.Context) {
	id, valid := parseID(c)
	if !valid {
		return
	}
	var input struct {
		VersionLabel string `json:"version_label"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 2000)
	if c.ShouldBindJSON(&input) != nil || len(input.VersionLabel) > 255 {
		fail(c, 422, "VALIDATION_FAILED", "invalid version label", nil)
		return
	}
	var source domain.AuthoritySource
	if lookupFailed(c, s.store.DB.WithContext(c.Request.Context()).Where("id=? AND status='registered'", id).First(&source).Error, "could not load registered source") {
		return
	}
	ttl := s.authoritySnapshotTTL
	if ttl == 0 {
		ttl = 720 * time.Hour
	}
	if ttl <= 0 || ttl > 365*24*time.Hour {
		fail(c, 500, "INVALID_CONFIGURATION", "invalid authority snapshot lifetime", nil)
		return
	}
	snapshot, err := authority.Fetch(c.Request.Context(), source, input.VersionLabel)
	if err != nil {
		fail(c, 422, "SOURCE_FETCH_FAILED", err.Error(), nil)
		return
	}
	snapshot.ExpiresAt = snapshot.AccessedAt.Add(ttl)
	if err := s.store.DB.WithContext(c.Request.Context()).Create(&snapshot).Error; err != nil {
		fail(c, 500, "INTERNAL_ERROR", "could not preserve source snapshot", nil)
		return
	}
	ok(c, 201, snapshot)
}

func (s *Server) getAuthoritySnapshot(c *gin.Context) {
	id, valid := parseID(c)
	if !valid {
		return
	}
	var row domain.AuthoritySnapshot
	if lookupFailed(c, s.store.DB.WithContext(c.Request.Context()).First(&row, id).Error, "could not load authority snapshot") {
		return
	}
	sum := sha256.Sum256([]byte(row.Content))
	textSum := sha256.Sum256([]byte(row.ExtractedText))
	if hex.EncodeToString(sum[:]) != row.ContentHash || hex.EncodeToString(textSum[:]) != row.TextHash {
		fail(c, 410, "EVIDENCE_INVALID", "authority snapshot hash mismatch", nil)
		return
	}
	ok(c, 200, gin.H{"snapshot": row, "expired": time.Now().UTC().After(row.ExpiresAt), "version_known": row.VersionLabel != ""})
}

func (s *Server) listAuthoritySnapshots(c *gin.Context) {
	id, valid := parseID(c)
	if !valid {
		return
	}
	rows := []map[string]any{}
	if err := s.store.DB.WithContext(c.Request.Context()).Table("authority_snapshots").Select("id,authority_source_id,final_url,title,version_label,accessed_at,expires_at,content_hash,text_hash").Where("authority_source_id=?", id).Order("id DESC").Limit(20).Find(&rows).Error; err != nil {
		fail(c, 500, "INTERNAL_ERROR", "could not load source snapshots", nil)
		return
	}
	ok(c, 200, rows)
}

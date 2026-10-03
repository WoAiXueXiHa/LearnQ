package domain

import "time"

// ArticleImage binds an image occurrence to immutable source bytes. ImageID
// references the existing snapshot and description task, shared across uses.
type ArticleImage struct {
	ID          uint64    `json:"id"`
	DocumentID  uint64    `json:"document_id"`
	IndexID     uint64    `json:"index_id"`
	StartByte   int       `json:"start_byte"`
	EndByte     int       `json:"end_byte"`
	StartLine   int       `json:"start_line"`
	EndLine     int       `json:"end_line"`
	OriginalURL string    `json:"original_url"`
	AltText     string    `json:"alt_text"`
	SyntaxKind  string    `json:"syntax_kind"`
	Status      string    `json:"status"`
	ImageID     *uint64   `json:"image_id,omitempty"`
	LastError   string    `json:"last_error,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	EvidenceURL string    `json:"evidence_url,omitempty" gorm:"-"`
}

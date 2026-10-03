package domain

import "time"

type AuthoritySource struct {
	ID        uint64    `json:"id"`
	Topic     string    `json:"topic"`
	URL       string    `json:"url"`
	Hostname  string    `json:"hostname"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

type AuthoritySnapshot struct {
	ID                uint64    `json:"id"`
	AuthoritySourceID uint64    `json:"authority_source_id"`
	FinalURL          string    `json:"final_url"`
	Title             string    `json:"title"`
	VersionLabel      string    `json:"version_label"`
	Content           string    `json:"-"`
	ExtractedText     string    `json:"extracted_text"`
	ContentHash       string    `json:"content_hash"`
	TextHash          string    `json:"text_hash"`
	AccessedAt        time.Time `json:"accessed_at"`
	ExpiresAt         time.Time `json:"expires_at"`
}

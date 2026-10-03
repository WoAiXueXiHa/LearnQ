package domain

import "time"

type AuthorityClaim struct {
	ID               uint64    `json:"id"`
	AnswerFeedbackID uint64    `json:"answer_feedback_id"`
	ItemOrdinal      int       `json:"item_ordinal"`
	Assertion        string    `json:"assertion"`
	Status           string    `json:"status"`
	CreatedAt        time.Time `json:"created_at"`
}

type AuthorityCheck struct {
	IdempotencyKey      *string   `json:"-"`
	ID                  uint64    `json:"id"`
	AuthorityClaimID    uint64    `json:"authority_claim_id"`
	AuthoritySnapshotID uint64    `json:"authority_snapshot_id"`
	Excerpt             string    `json:"excerpt"`
	ContextNote         string    `json:"context_note"`
	Status              string    `json:"status"`
	Judgment            string    `json:"judgment"`
	Explanation         string    `json:"explanation"`
	Model               string    `json:"model"`
	OriginalResponse    string    `json:"original_response,omitempty"`
	TaskID              uint64    `json:"task_id"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

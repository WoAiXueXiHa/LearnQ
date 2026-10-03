package domain

import "time"

type AuthorityCheckReview struct {
	ID               uint64    `json:"id"`
	AuthorityCheckID uint64    `json:"authority_check_id"`
	Disposition      string    `json:"disposition"`
	Comment          string    `json:"comment"`
	CreatedAt        time.Time `json:"created_at"`
}

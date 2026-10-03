package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/WoAiXueXiHa/LearnQ/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ValidatePracticeReview never converts disputed or unverified facts into a
// review conclusion. Call within the scheduling/starting transaction.
func (s *Store) ValidatePracticeReview(ctx context.Context, attemptID uint64) error {
	var feedback []domain.AnswerFeedback
	if err := s.DB.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("practice_attempt_id=?", attemptID).Order("ordinal").Find(&feedback).Error; err != nil {
		return err
	}
	if len(feedback) != 5 {
		return errors.New("五题反馈尚未完整，暂不能安排复习")
	}
	for _, row := range feedback {
		if row.Status != "ready" {
			return errors.New("存在未完成的反馈，暂不能安排复习")
		}
		var correction domain.FeedbackCorrection
		err := s.DB.WithContext(ctx).Where("answer_feedback_id=?", row.ID).Order("id DESC").First(&correction).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err == nil && correction.Disposition != "accepted" {
			return errors.New("存在被质疑或尚不确定的反馈，请先核对并记录最新意见")
		}
		var items []domain.FeedbackItem
		if json.Unmarshal([]byte(row.ItemsJSON), &items) != nil {
			return errors.New("反馈内容无法解析")
		}
		for ordinal, item := range items {
			if item.Type != "conflict" {
				if item.Type == "omission" && item.JudgmentStatus != "article_only" {
					return errors.New("遗漏判断缺少可确认依据，暂不能用于复习")
				}
				continue
			}
			var claim domain.AuthorityClaim
			if err := s.DB.WithContext(ctx).Where("answer_feedback_id=? AND item_ordinal=?", row.ID, ordinal+1).First(&claim).Error; err != nil {
				return errors.New("事实冲突尚未核查，暂不能用于复习")
			}
			var check domain.AuthorityCheck
			if err := s.DB.WithContext(ctx).Where("authority_claim_id=?", claim.ID).Order("id DESC").First(&check).Error; err != nil || check.Status != "ready" || check.Judgment == "uncertain" {
				return errors.New("事实冲突核查尚未完成或仍不确定")
			}
			var review domain.AuthorityCheckReview
			if err := s.DB.WithContext(ctx).Where("authority_check_id=?", check.ID).Order("id DESC").First(&review).Error; err != nil || review.Disposition != "confirmed" {
				return errors.New("事实冲突的外部依据与判断尚未由你确认")
			}
			var snapshot domain.AuthoritySnapshot
			if err := s.DB.WithContext(ctx).First(&snapshot, check.AuthoritySnapshotID).Error; err != nil {
				return err
			}
			if err := ValidateAuthorityEvidence(snapshot, check.Excerpt); err != nil {
				return errors.New("核查依据已过期或失效，请重新核查后复习")
			}
		}
	}
	return nil
}

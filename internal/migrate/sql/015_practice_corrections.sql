CREATE TABLE feedback_corrections (
 id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
 answer_feedback_id BIGINT UNSIGNED NOT NULL,
 original_items_json JSON NOT NULL,
 disposition VARCHAR(32) NOT NULL,
 comment TEXT NOT NULL,
 created_at DATETIME(6) NOT NULL,
 CONSTRAINT fk_feedback_correction FOREIGN KEY(answer_feedback_id) REFERENCES answer_feedback(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE practice_reviews (
 id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
 practice_attempt_id BIGINT UNSIGNED NOT NULL,
 question_set_id BIGINT UNSIGNED NOT NULL,
 status VARCHAR(24) NOT NULL,
 due_at DATETIME(6) NOT NULL,
 completed_attempt_id BIGINT UNSIGNED NULL,
 created_at DATETIME(6) NOT NULL,
 updated_at DATETIME(6) NOT NULL,
 UNIQUE KEY uq_practice_review(practice_attempt_id),
 CONSTRAINT fk_practice_review_attempt FOREIGN KEY(practice_attempt_id) REFERENCES practice_attempts(id),
 CONSTRAINT fk_practice_review_set FOREIGN KEY(question_set_id) REFERENCES question_sets(id),
 INDEX(status,due_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

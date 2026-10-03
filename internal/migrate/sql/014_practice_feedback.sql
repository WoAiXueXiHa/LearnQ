CREATE TABLE answer_feedback (
 id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
 practice_attempt_id BIGINT UNSIGNED NOT NULL,
 ordinal INT NOT NULL,
 task_id BIGINT UNSIGNED NOT NULL,
 status VARCHAR(32) NOT NULL,
 items_json JSON NOT NULL,
 original_json JSON NOT NULL,
 model VARCHAR(128) NOT NULL,
 prompt_version VARCHAR(64) NOT NULL,
 last_error TEXT NOT NULL,
 created_at DATETIME(6) NOT NULL,
 updated_at DATETIME(6) NOT NULL,
 UNIQUE KEY uq_answer_feedback(practice_attempt_id,ordinal),
 UNIQUE KEY uq_feedback_task(task_id),
 CONSTRAINT fk_answer_feedback_attempt FOREIGN KEY(practice_attempt_id) REFERENCES practice_attempts(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE authority_claims (
 id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
 answer_feedback_id BIGINT UNSIGNED NOT NULL,
 item_ordinal INT NOT NULL,
 assertion TEXT NOT NULL,
 status VARCHAR(32) NOT NULL,
 created_at DATETIME(6) NOT NULL,
 UNIQUE KEY uq_feedback_claim(answer_feedback_id,item_ordinal),
 CONSTRAINT fk_authority_claim_feedback FOREIGN KEY(answer_feedback_id) REFERENCES answer_feedback(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE authority_checks (
 id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
 authority_claim_id BIGINT UNSIGNED NOT NULL,
 authority_snapshot_id BIGINT UNSIGNED NOT NULL,
 excerpt TEXT NOT NULL,
 context_note TEXT NOT NULL,
 status VARCHAR(32) NOT NULL,
 judgment VARCHAR(32) NOT NULL,
 explanation TEXT NOT NULL,
 model VARCHAR(128) NOT NULL,
 task_id BIGINT UNSIGNED NOT NULL,
 created_at DATETIME(6) NOT NULL,
 updated_at DATETIME(6) NOT NULL,
 CONSTRAINT fk_authority_check_claim FOREIGN KEY(authority_claim_id) REFERENCES authority_claims(id),
 CONSTRAINT fk_authority_check_snapshot FOREIGN KEY(authority_snapshot_id) REFERENCES authority_snapshots(id),
 UNIQUE KEY uq_authority_check_task(task_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE question_sets (
 id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
 document_id BIGINT UNSIGNED NOT NULL,
 index_id BIGINT UNSIGNED NOT NULL,
 status VARCHAR(32) NOT NULL,
 model VARCHAR(128) NOT NULL,
 prompt_version VARCHAR(64) NOT NULL,
 original_json JSON NOT NULL,
 last_error TEXT NOT NULL,
 created_at DATETIME(6) NOT NULL,
 updated_at DATETIME(6) NOT NULL,
 INDEX(document_id),
 CONSTRAINT fk_question_set_index FOREIGN KEY(index_id) REFERENCES document_indexes(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE practice_questions (
 id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
 question_set_id BIGINT UNSIGNED NOT NULL,
 ordinal INT NOT NULL,
 prompt TEXT NOT NULL,
 knowledge_points_json JSON NOT NULL,
 reference_points_json JSON NOT NULL,
 evidence_json JSON NOT NULL,
 UNIQUE KEY uq_practice_question(question_set_id,ordinal),
 CONSTRAINT fk_practice_question_set FOREIGN KEY(question_set_id) REFERENCES question_sets(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE question_set_edits (
 id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
 question_set_id BIGINT UNSIGNED NOT NULL,
 questions_json JSON NOT NULL,
 created_at DATETIME(6) NOT NULL,
 CONSTRAINT fk_question_edit_set FOREIGN KEY(question_set_id) REFERENCES question_sets(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE practice_attempts (
 id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
 question_set_id BIGINT UNSIGNED NOT NULL,
 status VARCHAR(32) NOT NULL,
 answers_json JSON NOT NULL,
 submitted_at DATETIME(6) NULL,
 last_error TEXT NOT NULL,
 created_at DATETIME(6) NOT NULL,
 updated_at DATETIME(6) NOT NULL,
 INDEX(question_set_id),
 CONSTRAINT fk_practice_attempt_set FOREIGN KEY(question_set_id) REFERENCES question_sets(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

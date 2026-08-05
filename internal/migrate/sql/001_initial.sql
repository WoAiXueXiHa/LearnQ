CREATE TABLE study_records (
 id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY, title VARCHAR(255) NOT NULL,
 summary TEXT NOT NULL, duration_minute INT NOT NULL, created_at DATETIME(6) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE study_modules (
 id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY, study_record_id BIGINT UNSIGNED NOT NULL,
 category VARCHAR(64) NOT NULL, content TEXT NOT NULL, INDEX(study_record_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE ai_tasks (
 id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY, kind VARCHAR(64) NOT NULL, status VARCHAR(24) NOT NULL,
 payload_json JSON NOT NULL, attempt_no INT NOT NULL DEFAULT 0, execution_generation INT NOT NULL DEFAULT 1,
 lease_token VARCHAR(64) NOT NULL DEFAULT '', lease_until DATETIME(6), available_at DATETIME(6) NOT NULL,
 last_error TEXT NOT NULL, created_at DATETIME(6) NOT NULL, updated_at DATETIME(6) NOT NULL,
 INDEX idx_tasks_dispatch(status,available_at), INDEX idx_tasks_lease(status,lease_until)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE outbox_events (
 id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY, aggregate_id BIGINT UNSIGNED NOT NULL,
 event_type VARCHAR(64) NOT NULL, payload_json JSON NOT NULL, published_at DATETIME(6), created_at DATETIME(6) NOT NULL,
 INDEX idx_outbox_unpublished(published_at,id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE task_attempts (
 id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY, task_id BIGINT UNSIGNED NOT NULL,
 execution_generation INT NOT NULL, attempt_no INT NOT NULL, lease_token VARCHAR(64) NOT NULL,
 status VARCHAR(24) NOT NULL, error_message TEXT NOT NULL, started_at DATETIME(6) NOT NULL, finished_at DATETIME(6),
 UNIQUE KEY uq_attempt(task_id,execution_generation,attempt_no)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE reports (
 id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY, task_id BIGINT UNSIGNED NOT NULL,
 markdown_content LONGTEXT NOT NULL, export_status VARCHAR(24) NOT NULL, export_error TEXT NOT NULL,
 created_at DATETIME(6) NOT NULL, UNIQUE KEY uq_report_task(task_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE review_tasks (
 id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY, report_id BIGINT UNSIGNED NOT NULL, mastery INT NOT NULL,
 status VARCHAR(24) NOT NULL, due_at DATETIME(6) NOT NULL, completed_at DATETIME(6),
 created_at DATETIME(6) NOT NULL, updated_at DATETIME(6) NOT NULL, INDEX idx_reviews_due(status,due_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE review_events (
 id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY, review_task_id BIGINT UNSIGNED NOT NULL, action VARCHAR(24) NOT NULL,
 old_mastery INT NOT NULL, new_mastery INT NOT NULL, old_due_at DATETIME(6) NOT NULL,
 new_due_at DATETIME(6) NOT NULL, created_at DATETIME(6) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE idempotency_keys (
 id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY, scope VARCHAR(255) NOT NULL, request_hash CHAR(64) NOT NULL,
 status_code INT NOT NULL, response_json JSON NOT NULL, created_at DATETIME(6) NOT NULL,
 UNIQUE KEY uq_idempotency_scope(scope)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

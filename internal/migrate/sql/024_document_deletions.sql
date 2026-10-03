CREATE TABLE document_deletions (
 id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
 document_id BIGINT UNSIGNED NOT NULL,
 status VARCHAR(32) NOT NULL,
 quiesce_until DATETIME(6) NOT NULL,
 plan_hash CHAR(64) NOT NULL,
 plan_json JSON NOT NULL,
 image_ids_json JSON NOT NULL,
 last_error TEXT NOT NULL,
 created_at DATETIME(6) NOT NULL,
 updated_at DATETIME(6) NOT NULL,
 UNIQUE KEY uq_document_deletion(document_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

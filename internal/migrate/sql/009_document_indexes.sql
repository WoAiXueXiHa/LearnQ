ALTER TABLE documents ADD COLUMN active_index_id BIGINT UNSIGNED NOT NULL DEFAULT 0;
CREATE TABLE document_indexes (
 id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
 document_id BIGINT UNSIGNED NOT NULL,
 task_id BIGINT UNSIGNED NOT NULL,
 execution_generation INT NOT NULL,
 attempt_no INT NOT NULL,
 content LONGTEXT NOT NULL,
 content_hash CHAR(64) NOT NULL,
 index_version VARCHAR(512) NOT NULL,
 chunk_version VARCHAR(64) NOT NULL,
 dimension INT NOT NULL,
 status VARCHAR(24) NOT NULL,
 created_at DATETIME(6) NOT NULL,
 UNIQUE KEY uq_index_attempt(task_id, execution_generation, attempt_no),
 INDEX(document_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
ALTER TABLE document_chunks
 ADD COLUMN index_id BIGINT UNSIGNED NOT NULL DEFAULT 0,
 ADD COLUMN start_byte INT NOT NULL DEFAULT -1,
 ADD COLUMN end_byte INT NOT NULL DEFAULT -1,
 DROP INDEX uq_document_chunk,
 ADD UNIQUE KEY uq_document_index_chunk(document_id,index_id,chunk_index),
 ADD INDEX(index_id);

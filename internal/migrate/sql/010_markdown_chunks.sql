ALTER TABLE document_chunks
 ADD COLUMN embedding_content TEXT NULL,
 ADD COLUMN heading_path_json TEXT NULL,
 ADD COLUMN block_type VARCHAR(32) NOT NULL DEFAULT '',
 ADD COLUMN image_refs_json LONGTEXT NULL,
 ADD COLUMN block_spans_json LONGTEXT NULL;

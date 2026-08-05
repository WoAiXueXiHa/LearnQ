ALTER TABLE study_records
  ADD COLUMN content_fingerprint CHAR(64) NOT NULL DEFAULT '' AFTER duration_minute,
  ADD INDEX idx_study_fingerprint_created(content_fingerprint, created_at);

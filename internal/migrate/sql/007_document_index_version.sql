ALTER TABLE documents
  ADD COLUMN index_version VARCHAR(512) NOT NULL DEFAULT '' AFTER status;

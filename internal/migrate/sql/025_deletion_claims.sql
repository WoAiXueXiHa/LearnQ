ALTER TABLE document_deletions
  ADD COLUMN claim_token VARCHAR(32) NOT NULL DEFAULT '',
  ADD COLUMN claim_until DATETIME(6) NULL;

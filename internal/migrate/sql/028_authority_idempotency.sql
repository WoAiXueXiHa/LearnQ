ALTER TABLE authority_checks
 ADD COLUMN idempotency_key VARCHAR(64) NULL,
 ADD UNIQUE KEY uq_authority_check_request(idempotency_key);

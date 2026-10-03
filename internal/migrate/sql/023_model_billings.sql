CREATE TABLE model_call_billings (
 id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
 model_call_id BIGINT UNSIGNED NOT NULL,
 billed_microcny BIGINT NOT NULL,
 note VARCHAR(1024) NOT NULL,
 created_at DATETIME(6) NOT NULL,
 CONSTRAINT fk_model_billing_call FOREIGN KEY(model_call_id) REFERENCES model_calls(id),
 INDEX(model_call_id,id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE model_prices (
 id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
 model VARCHAR(128) NOT NULL,
 input_microcny_per_million BIGINT NOT NULL,
 output_microcny_per_million BIGINT NOT NULL,
 max_input_tokens INT NOT NULL,
 max_output_tokens INT NOT NULL,
 version_label VARCHAR(255) NOT NULL,
 created_at DATETIME(6) NOT NULL,
 INDEX(model,id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
ALTER TABLE model_calls
 ADD COLUMN model_price_id BIGINT UNSIGNED NULL,
 ADD COLUMN estimated_microcny BIGINT NULL,
 ADD COLUMN estimate_basis VARCHAR(64) NOT NULL DEFAULT 'unavailable';

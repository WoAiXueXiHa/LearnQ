CREATE TABLE model_daily_budgets (
 day DATE PRIMARY KEY,
 reserved_microcny BIGINT NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE model_calls (
 id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
 kind VARCHAR(64) NOT NULL,
 mode VARCHAR(16) NOT NULL,
 model VARCHAR(128) NOT NULL,
 status VARCHAR(24) NOT NULL,
 reserved_microcny BIGINT NOT NULL,
 input_tokens BIGINT NOT NULL,
 output_tokens BIGINT NOT NULL,
	usage_known BOOLEAN NOT NULL DEFAULT FALSE,
 duration_ms BIGINT NOT NULL,
 error_code VARCHAR(64) NOT NULL,
 started_at DATETIME(6) NOT NULL,
 finished_at DATETIME(6) NULL,
 INDEX(started_at),
 INDEX(status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

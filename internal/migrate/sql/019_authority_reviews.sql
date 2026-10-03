CREATE TABLE authority_check_reviews (
 id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
 authority_check_id BIGINT UNSIGNED NOT NULL,
 disposition VARCHAR(32) NOT NULL,
 comment TEXT NOT NULL,
 created_at DATETIME(6) NOT NULL,
 CONSTRAINT fk_authority_review_check FOREIGN KEY(authority_check_id) REFERENCES authority_checks(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE authority_sources (
 id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
 topic VARCHAR(255) NOT NULL,
 url TEXT NOT NULL,
 hostname VARCHAR(255) NOT NULL,
 status VARCHAR(24) NOT NULL,
 created_at DATETIME(6) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE authority_snapshots (
 id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
 authority_source_id BIGINT UNSIGNED NOT NULL,
 final_url TEXT NOT NULL,
 title TEXT NOT NULL,
 version_label VARCHAR(255) NOT NULL,
 content LONGTEXT NOT NULL,
 extracted_text LONGTEXT NOT NULL,
 content_hash CHAR(64) NOT NULL,
	text_hash CHAR(64) NOT NULL,
 accessed_at DATETIME(6) NOT NULL,
 expires_at DATETIME(6) NOT NULL,
 CONSTRAINT fk_authority_snapshot_source FOREIGN KEY(authority_source_id) REFERENCES authority_sources(id),
 INDEX(authority_source_id,accessed_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

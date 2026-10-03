CREATE TABLE image_description_corrections (
 id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
 document_id BIGINT UNSIGNED NOT NULL,
 image_hash VARCHAR(64) NOT NULL,
 description_json MEDIUMTEXT NOT NULL,
 comment TEXT NOT NULL,
 created_at DATETIME(6) NOT NULL,
 INDEX ix_image_correction_version(document_id,image_hash,created_at,id),
 CONSTRAINT fk_image_correction_document FOREIGN KEY(document_id) REFERENCES documents(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

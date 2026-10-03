CREATE TABLE image_description_cache (
 id CHAR(64) PRIMARY KEY,
 image_id BIGINT UNSIGNED NULL,
 CONSTRAINT fk_image_description_cache FOREIGN KEY(image_id) REFERENCES images(id) ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

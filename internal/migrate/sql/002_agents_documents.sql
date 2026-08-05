CREATE TABLE agent_runs (
 id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY, task_id BIGINT UNSIGNED, skill_name VARCHAR(64) NOT NULL,
 skill_version VARCHAR(24) NOT NULL, prompt_version VARCHAR(24) NOT NULL, prompt_hash CHAR(64) NOT NULL,
 schema_version VARCHAR(24) NOT NULL, model_name VARCHAR(128) NOT NULL, input_summary TEXT NOT NULL,
 output_json LONGTEXT NOT NULL, input_tokens INT NOT NULL, output_tokens INT NOT NULL,
 latency_ms BIGINT NOT NULL, error_reason TEXT NOT NULL, created_at DATETIME(6) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE agent_steps (
 id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY, agent_run_id BIGINT UNSIGNED NOT NULL,
 agent_name VARCHAR(64) NOT NULL, step_name VARCHAR(64) NOT NULL, output_json LONGTEXT NOT NULL,
 latency_ms BIGINT NOT NULL, error_reason TEXT NOT NULL, created_at DATETIME(6) NOT NULL, INDEX(agent_run_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE tool_calls (
 id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY, agent_run_id BIGINT UNSIGNED NOT NULL, tool_name VARCHAR(64) NOT NULL,
 request_json LONGTEXT NOT NULL, response_json LONGTEXT NOT NULL, retrieval_citations_json LONGTEXT NOT NULL,
 latency_ms BIGINT NOT NULL, error_reason TEXT NOT NULL, created_at DATETIME(6) NOT NULL, INDEX(agent_run_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE documents (
 id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY, filename VARCHAR(255) NOT NULL, media_type VARCHAR(64) NOT NULL,
 content_hash CHAR(64) NOT NULL, content LONGTEXT NOT NULL, status VARCHAR(24) NOT NULL,
 indexing_task_id BIGINT UNSIGNED, error_message TEXT NOT NULL, created_at DATETIME(6) NOT NULL,
 updated_at DATETIME(6) NOT NULL, INDEX(status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE document_chunks (
 id CHAR(64) NOT NULL PRIMARY KEY, document_id BIGINT UNSIGNED NOT NULL, chunk_index INT NOT NULL,
 title VARCHAR(255) NOT NULL, start_line INT NOT NULL, end_line INT NOT NULL, content TEXT NOT NULL,
 content_hash CHAR(64) NOT NULL, created_at DATETIME(6) NOT NULL,
 UNIQUE KEY uq_document_chunk(document_id,chunk_index), INDEX(document_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE rag_evaluations (
 id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY, mode VARCHAR(32) NOT NULL, dataset_version VARCHAR(64) NOT NULL,
 embedding_model VARCHAR(128) NOT NULL, collection_name VARCHAR(128) NOT NULL, top_k INT NOT NULL,
 config_json JSON NOT NULL, metrics_json JSON NOT NULL, report_markdown LONGTEXT NOT NULL,
 status VARCHAR(24) NOT NULL, created_at DATETIME(6) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

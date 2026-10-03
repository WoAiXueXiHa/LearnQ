CREATE TABLE practice_model_results (
 task_id BIGINT UNSIGNED NOT NULL,
 execution_generation BIGINT NOT NULL,
 input_hash VARCHAR(64) NOT NULL,
 response_json MEDIUMTEXT NOT NULL,
 response_hash VARCHAR(64) NOT NULL,
 created_at DATETIME(6) NOT NULL,
 PRIMARY KEY(task_id,execution_generation),
 CONSTRAINT fk_practice_result_task FOREIGN KEY(task_id) REFERENCES ai_tasks(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

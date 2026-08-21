ALTER TABLE agent_runs
  ADD COLUMN task_type VARCHAR(32) NOT NULL DEFAULT 'legacy',
  ADD COLUMN status VARCHAR(24) NOT NULL DEFAULT 'succeeded',
  ADD COLUMN plan_json JSON NOT NULL,
  ADD COLUMN self_check_json JSON NOT NULL,
  ADD COLUMN completed_at DATETIME(6) NULL,
  ADD INDEX idx_agent_runs_task_type_created(task_type, created_at);

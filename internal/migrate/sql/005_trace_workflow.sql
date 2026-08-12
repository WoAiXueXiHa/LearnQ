ALTER TABLE agent_runs ADD COLUMN workflow_id VARCHAR(64) NULL, ADD INDEX idx_agent_runs_workflow_id (workflow_id);

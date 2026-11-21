-- Rollback task_runs
DROP INDEX IF EXISTS idx_prompt_events_task_run;
ALTER TABLE prompt_events DROP COLUMN task_run_id;

DROP INDEX IF EXISTS idx_task_runs_start_time;
DROP INDEX IF EXISTS idx_task_runs_task_id;
DROP INDEX IF EXISTS idx_task_runs_repo;
DROP TABLE IF EXISTS task_runs;

-- Task runs table: runner-level task lifecycle (one row per attempt)
CREATE TABLE task_runs (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL,
    repo TEXT NOT NULL,
    agent TEXT NOT NULL,
    harness_git_hash TEXT NOT NULL,
    start_time INTEGER NOT NULL,
    end_time INTEGER,
    outcome TEXT,
    retry_count INTEGER NOT NULL DEFAULT 0,
    base_commit TEXT,
    final_commit TEXT,
    diff_stored BOOLEAN DEFAULT FALSE,
    verify_pass BOOLEAN,
    verify_summary TEXT,
    pr_url TEXT
);

CREATE INDEX idx_task_runs_repo ON task_runs(repo);
CREATE INDEX idx_task_runs_task_id ON task_runs(task_id);
CREATE INDEX idx_task_runs_start_time ON task_runs(start_time);

-- Link prompt events to the task run that produced them (nullable)
ALTER TABLE prompt_events ADD COLUMN task_run_id TEXT REFERENCES task_runs(id);
CREATE INDEX idx_prompt_events_task_run ON prompt_events(task_run_id);

package storage

import (
	"database/sql"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestNewUUID(t *testing.T) {
	a, err := NewUUID()
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewUUID()
	if err != nil {
		t.Fatal(err)
	}
	if !uuidRe.MatchString(a) {
		t.Errorf("not a v4 uuid: %q", a)
	}
	if a == b {
		t.Errorf("expected distinct uuids, got %q twice", a)
	}
}

func TestCreateTaskRun(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	start := time.Unix(1700000000, 0)
	run := &TaskRun{
		TaskID:         "42",
		Repo:           "owner/repo",
		Agent:          "claude",
		HarnessGitHash: "deadbeef",
		StartTime:      start,
		RetryCount:     1,
		BaseCommit:     "abc123",
	}
	if err := CreateTaskRun(db, run); err != nil {
		t.Fatalf("CreateTaskRun: %v", err)
	}
	if !uuidRe.MatchString(run.ID) {
		t.Fatalf("expected generated uuid, got %q", run.ID)
	}

	got, err := GetTaskRun(db, run.ID)
	if err != nil {
		t.Fatalf("GetTaskRun: %v", err)
	}
	if got.TaskID != "42" || got.Repo != "owner/repo" || got.Agent != "claude" ||
		got.HarnessGitHash != "deadbeef" || got.RetryCount != 1 || got.BaseCommit != "abc123" {
		t.Errorf("unexpected run: %+v", got)
	}
	if !got.StartTime.Equal(start) {
		t.Errorf("start time = %v, want %v", got.StartTime, start)
	}
	if got.EndTime != nil || got.Outcome != "" || got.VerifyPass != nil || got.DiffStored {
		t.Errorf("expected incomplete run, got %+v", got)
	}

	if _, err := GetTaskRun(db, "missing"); err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestCompleteTaskRun(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	run := &TaskRun{TaskID: "7", Repo: "o/r", Agent: "claude", HarnessGitHash: "h"}
	if err := CreateTaskRun(db, run); err != nil {
		t.Fatal(err)
	}

	before := time.Now().Add(-time.Second)
	summary := `{"build":"ok","tests":"ok"}`
	if err := CompleteTaskRun(db, run.ID, "complete", "fff000", true, summary, "https://github.com/o/r/pull/1"); err != nil {
		t.Fatalf("CompleteTaskRun: %v", err)
	}

	got, err := GetTaskRun(db, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Outcome != "complete" || got.FinalCommit != "fff000" ||
		got.VerifySummary != summary || got.PRURL != "https://github.com/o/r/pull/1" {
		t.Errorf("unexpected completed run: %+v", got)
	}
	if got.VerifyPass == nil || !*got.VerifyPass {
		t.Errorf("expected verify_pass true, got %v", got.VerifyPass)
	}
	if got.EndTime == nil || got.EndTime.Before(before) {
		t.Errorf("expected end_time set to now, got %v", got.EndTime)
	}

	if err := CompleteTaskRun(db, "missing", "complete", "", false, "", ""); err != ErrNotFound {
		t.Errorf("expected ErrNotFound for missing run, got %v", err)
	}
}

func TestCompleteTaskRunWith(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	run := &TaskRun{TaskID: "7", Repo: "o/r", Agent: "claude", HarnessGitHash: "h"}
	if err := CreateTaskRun(db, run); err != nil {
		t.Fatal(err)
	}

	if err := CompleteTaskRunWith(db, run.ID, TaskRunCompletion{Outcome: "timeout", DiffStored: true}); err != nil {
		t.Fatal(err)
	}
	got, err := GetTaskRun(db, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Outcome != "timeout" || !got.DiffStored || got.VerifyPass != nil || got.FinalCommit != "" {
		t.Errorf("unexpected run: %+v", got)
	}
}

func TestListTaskRuns(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	base := time.Unix(1700000000, 0)
	runs := []*TaskRun{
		{TaskID: "1", Repo: "o/a", StartTime: base},
		{TaskID: "2", Repo: "o/a", StartTime: base.Add(1 * time.Minute)},
		{TaskID: "1", Repo: "o/a", StartTime: base.Add(2 * time.Minute), RetryCount: 1},
		{TaskID: "1", Repo: "o/b", StartTime: base.Add(3 * time.Minute)},
	}
	for _, r := range runs {
		r.Agent = "claude"
		r.HarnessGitHash = "h"
		if err := CreateTaskRun(db, r); err != nil {
			t.Fatal(err)
		}
	}

	all, err := ListTaskRuns(db, "o/a", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 runs for o/a, got %d", len(all))
	}
	if all[0].ID != runs[2].ID || all[2].ID != runs[0].ID {
		t.Errorf("expected newest first ordering")
	}

	task1, err := ListTaskRuns(db, "o/a", "1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(task1) != 2 {
		t.Fatalf("expected 2 attempts for task 1, got %d", len(task1))
	}
	for _, r := range task1 {
		if r.TaskID != "1" || r.Repo != "o/a" {
			t.Errorf("unexpected run in filtered list: %+v", r)
		}
	}

	limited, err := ListTaskRuns(db, "o/a", "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 2 {
		t.Errorf("expected limit of 2, got %d", len(limited))
	}

	none, err := ListTaskRuns(db, "o/none", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Errorf("expected no runs, got %d", len(none))
	}
}

func TestPromptEventTaskRunID(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	if err := CreateSession(db, &Session{ID: "s1", StartTime: time.Now(), RepoPath: "/r"}); err != nil {
		t.Fatal(err)
	}
	run := &TaskRun{TaskID: "1", Repo: "o/r", Agent: "claude", HarnessGitHash: "h"}
	if err := CreateTaskRun(db, run); err != nil {
		t.Fatal(err)
	}

	with := &PromptEvent{ID: "e1", Timestamp: time.Now(), SessionID: "s1", Agent: "claude-code",
		PromptText: "x", RepoPath: "/r", Author: "a", TaskRunID: run.ID}
	without := &PromptEvent{ID: "e2", Timestamp: time.Now(), SessionID: "s1", Agent: "claude-code",
		PromptText: "y", RepoPath: "/r", Author: "a"}
	for _, e := range []*PromptEvent{with, without} {
		if err := StorePromptEvent(db, e); err != nil {
			t.Fatal(err)
		}
	}

	var id sql.NullString
	if err := db.QueryRow("SELECT task_run_id FROM prompt_events WHERE id = 'e1'").Scan(&id); err != nil {
		t.Fatal(err)
	}
	if !id.Valid || id.String != run.ID {
		t.Errorf("expected task_run_id %q, got %+v", run.ID, id)
	}
	if err := db.QueryRow("SELECT task_run_id FROM prompt_events WHERE id = 'e2'").Scan(&id); err != nil {
		t.Fatal(err)
	}
	if id.Valid {
		t.Errorf("expected NULL task_run_id, got %q", id.String)
	}

	got, err := GetPromptEvent(db, "e1")
	if err != nil {
		t.Fatal(err)
	}
	if got.TaskRunID != run.ID {
		t.Errorf("GetPromptEvent TaskRunID = %q, want %q", got.TaskRunID, run.ID)
	}
}

func TestBusyTimeoutSet(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	db.SetMaxOpenConns(1)

	var timeout int
	if err := db.QueryRow("PRAGMA busy_timeout").Scan(&timeout); err != nil {
		t.Fatal(err)
	}
	if timeout != 5000 {
		t.Errorf("busy_timeout = %d, want 5000", timeout)
	}
}

// TestMigration000002PreservesData creates a database at schema version 1 with
// existing rows, then opens it with InitDatabase and verifies the data survives
// and the new schema is present.
func TestMigration000002PreservesData(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "v1.db")

	raw, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	v1, err := migrationsFS.ReadFile("migrations/000001_initial_schema.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	stmts := []string{
		string(v1),
		`CREATE TABLE schema_migrations (version uint64, dirty bool)`,
		`CREATE UNIQUE INDEX version_unique ON schema_migrations (version)`,
		`INSERT INTO schema_migrations (version, dirty) VALUES (1, 0)`,
		`INSERT INTO sessions (id, start_time, repo_path) VALUES ('s1', 1, '/r')`,
		`INSERT INTO prompt_events (id, timestamp, session_id, agent, model_version, prompt_text, response_text,
			tokens_in, tokens_out, latency_ms, repo_path, git_commit, git_branch, git_dirty, dirty_files,
			author, ide, active_file, workspace_files, prompt_type, tools_invoked, files_mentioned)
		 VALUES ('old', 1, 's1', 'claude-code', '', 'hello', '', 0, 0, 0, '/r', '', '', 0, '[]',
			'a', '', '', '[]', '', '[]', '[]')`,
	}
	for _, s := range stmts {
		if _, err := raw.Exec(s); err != nil {
			t.Fatalf("setup v1 schema: %v", err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := InitDatabase(dbPath)
	if err != nil {
		t.Fatalf("InitDatabase on v1 db: %v", err)
	}
	defer db.Close()

	var version int
	if err := db.QueryRow("SELECT version FROM schema_migrations").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 2 {
		t.Errorf("schema version = %d, want 2", version)
	}

	ev, err := GetPromptEvent(db, "old")
	if err != nil {
		t.Fatalf("existing event lost after migration: %v", err)
	}
	if ev.PromptText != "hello" || ev.TaskRunID != "" {
		t.Errorf("unexpected migrated event: %+v", ev)
	}
	if !tableExists(t, db, "task_runs") {
		t.Error("task_runs table missing after migration")
	}
	if !contains(getTableColumns(t, db, "prompt_events"), "task_run_id") {
		t.Error("prompt_events.task_run_id missing after migration")
	}
}

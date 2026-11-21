package storage

import (
	"crypto/rand"
	"database/sql"
	"fmt"
	"time"
)

// TaskRun represents a single runner attempt at a task (e.g. a GitHub issue).
// Multiple TaskRuns may share a TaskID when a task is retried.
type TaskRun struct {
	ID             string
	TaskID         string // GitHub issue number, e.g. "42"
	Repo           string // owner/repo
	Agent          string
	HarnessGitHash string
	StartTime      time.Time
	EndTime        *time.Time // nil while running
	Outcome        string     // complete|partial|blocked|needs-info|escalated|timeout
	RetryCount     int
	BaseCommit     string
	FinalCommit    string
	DiffStored     bool
	VerifyPass     *bool // nil if verification was not reported
	VerifySummary  string
	PRURL          string
}

// TaskRunCompletion holds the fields recorded when a task run finishes.
type TaskRunCompletion struct {
	Outcome       string
	FinalCommit   string
	VerifyPass    *bool
	VerifySummary string
	PRURL         string
	DiffStored    bool
}

// NewUUID returns a random RFC 4122 version 4 UUID string.
func NewUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("failed to generate uuid: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

func nullString(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

// CreateTaskRun inserts a new task run. If run.ID is empty a UUID is generated
// and assigned. If run.StartTime is zero the current time is used.
func CreateTaskRun(db *sql.DB, run *TaskRun) error {
	if run.ID == "" {
		id, err := NewUUID()
		if err != nil {
			return err
		}
		run.ID = id
	}
	if run.StartTime.IsZero() {
		run.StartTime = time.Now()
	}

	var endTime sql.NullInt64
	if run.EndTime != nil {
		endTime = sql.NullInt64{Int64: run.EndTime.Unix(), Valid: true}
	}
	var verifyPass sql.NullBool
	if run.VerifyPass != nil {
		verifyPass = sql.NullBool{Bool: *run.VerifyPass, Valid: true}
	}

	query := `
		INSERT INTO task_runs (
			id, task_id, repo, agent, harness_git_hash,
			start_time, end_time, outcome, retry_count,
			base_commit, final_commit, diff_stored,
			verify_pass, verify_summary, pr_url
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	_, err := db.Exec(query,
		run.ID, run.TaskID, run.Repo, run.Agent, run.HarnessGitHash,
		run.StartTime.Unix(), endTime, nullString(run.Outcome), run.RetryCount,
		nullString(run.BaseCommit), nullString(run.FinalCommit), run.DiffStored,
		verifyPass, nullString(run.VerifySummary), nullString(run.PRURL),
	)
	if err != nil {
		return fmt.Errorf("failed to insert task run: %w", err)
	}
	return nil
}

// CompleteTaskRun records the outcome of a task run and sets end_time to now.
// diff_stored is left unchanged; use CompleteTaskRunWith to set it.
func CompleteTaskRun(db *sql.DB, id string, outcome string, finalCommit string, verifyPass bool, verifySummary string, prURL string) error {
	return completeTaskRun(db, id, TaskRunCompletion{
		Outcome:       outcome,
		FinalCommit:   finalCommit,
		VerifyPass:    &verifyPass,
		VerifySummary: verifySummary,
		PRURL:         prURL,
	}, false)
}

// CompleteTaskRunWith records the outcome of a task run, including diff_stored,
// and allows verify_pass to be left NULL when verification was not reported.
func CompleteTaskRunWith(db *sql.DB, id string, c TaskRunCompletion) error {
	return completeTaskRun(db, id, c, true)
}

func completeTaskRun(db *sql.DB, id string, c TaskRunCompletion, setDiffStored bool) error {
	var verifyPass sql.NullBool
	if c.VerifyPass != nil {
		verifyPass = sql.NullBool{Bool: *c.VerifyPass, Valid: true}
	}

	query := `
		UPDATE task_runs
		SET end_time = ?, outcome = ?, final_commit = ?,
		    verify_pass = ?, verify_summary = ?, pr_url = ?`
	args := []interface{}{
		time.Now().Unix(), c.Outcome, nullString(c.FinalCommit),
		verifyPass, nullString(c.VerifySummary), nullString(c.PRURL),
	}
	if setDiffStored {
		query += `, diff_stored = ?`
		args = append(args, c.DiffStored)
	}
	query += ` WHERE id = ?`
	args = append(args, id)

	res, err := db.Exec(query, args...)
	if err != nil {
		return fmt.Errorf("failed to complete task run: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to complete task run: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

const taskRunColumns = `
	id, task_id, repo, agent, harness_git_hash,
	start_time, end_time, outcome, retry_count,
	base_commit, final_commit, diff_stored,
	verify_pass, verify_summary, pr_url`

type rowScanner interface {
	Scan(dest ...interface{}) error
}

func scanTaskRun(row rowScanner) (*TaskRun, error) {
	var run TaskRun
	var startTime int64
	var endTime sql.NullInt64
	var outcome, baseCommit, finalCommit, verifySummary, prURL sql.NullString
	var diffStored, verifyPass sql.NullBool

	if err := row.Scan(
		&run.ID, &run.TaskID, &run.Repo, &run.Agent, &run.HarnessGitHash,
		&startTime, &endTime, &outcome, &run.RetryCount,
		&baseCommit, &finalCommit, &diffStored,
		&verifyPass, &verifySummary, &prURL,
	); err != nil {
		return nil, err
	}

	run.StartTime = time.Unix(startTime, 0)
	if endTime.Valid {
		t := time.Unix(endTime.Int64, 0)
		run.EndTime = &t
	}
	if verifyPass.Valid {
		v := verifyPass.Bool
		run.VerifyPass = &v
	}
	run.Outcome = outcome.String
	run.BaseCommit = baseCommit.String
	run.FinalCommit = finalCommit.String
	run.DiffStored = diffStored.Valid && diffStored.Bool
	run.VerifySummary = verifySummary.String
	run.PRURL = prURL.String
	return &run, nil
}

// GetTaskRun retrieves a task run by ID.
func GetTaskRun(db *sql.DB, id string) (*TaskRun, error) {
	row := db.QueryRow(`SELECT `+taskRunColumns+` FROM task_runs WHERE id = ?`, id)
	run, err := scanTaskRun(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to query task run: %w", err)
	}
	return run, nil
}

// ListTaskRuns returns task runs for a repo, newest first. If taskID is
// non-empty only attempts for that task are returned. A limit <= 0 means
// no limit.
func ListTaskRuns(db *sql.DB, repo string, taskID string, limit int) ([]*TaskRun, error) {
	query := `SELECT ` + taskRunColumns + ` FROM task_runs WHERE repo = ?`
	args := []interface{}{repo}
	if taskID != "" {
		query += ` AND task_id = ?`
		args = append(args, taskID)
	}
	query += ` ORDER BY start_time DESC, rowid DESC`
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query task runs: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var runs []*TaskRun
	for rows.Next() {
		run, err := scanTaskRun(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan task run: %w", err)
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating task runs: %w", err)
	}
	return runs, nil
}

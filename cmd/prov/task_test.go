package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/albachteng/provenance/internal/storage"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// runCLIStdout runs the CLI and returns stdout only.
func runCLIStdout(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	cmd := exec.Command(testBinary, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func startTestTaskRun(t *testing.T) string {
	t.Helper()
	out, errOut, err := runCLIStdout(t, "task", "start",
		"--task-id", "42",
		"--repo", "owner/repo",
		"--agent", "claude",
		"--harness-hash", "abc123",
		"--base-commit", "base000",
		"--retry-count", "1",
	)
	if err != nil {
		t.Fatalf("task start failed: %v\nstderr: %s", err, errOut)
	}
	id := strings.TrimSpace(out)
	if !uuidPattern.MatchString(id) {
		t.Fatalf("task start did not print a UUID, got %q", out)
	}
	return id
}

func TestCLITaskStartAndComplete(t *testing.T) {
	tmpDir := setupTestEnv(t)

	// No database exists yet and no daemon is running: task start must create it.
	id := startTestTaskRun(t)

	db := setupTestDB(t, tmpDir)
	defer db.Close()

	run, err := storage.GetTaskRun(db, id)
	if err != nil {
		t.Fatalf("GetTaskRun: %v", err)
	}
	if run.TaskID != "42" || run.Repo != "owner/repo" || run.Agent != "claude" ||
		run.HarnessGitHash != "abc123" || run.BaseCommit != "base000" || run.RetryCount != 1 {
		t.Errorf("unexpected task run: %+v", run)
	}
	if run.EndTime != nil {
		t.Errorf("expected nil end_time before completion")
	}

	summary := `{"build":"ok","vet":"ok"}`
	_, errOut, err := runCLIStdout(t, "task", "complete",
		"--run-id", id,
		"--outcome", "complete",
		"--final-commit", "final111",
		"--verify-pass", "true",
		"--verify-summary", summary,
		"--pr-url", "https://github.com/owner/repo/pull/9",
		"--diff-stored", "true",
	)
	if err != nil {
		t.Fatalf("task complete failed: %v\nstderr: %s", err, errOut)
	}

	run, err = storage.GetTaskRun(db, id)
	if err != nil {
		t.Fatal(err)
	}
	if run.Outcome != "complete" || run.FinalCommit != "final111" || run.VerifySummary != summary ||
		run.PRURL != "https://github.com/owner/repo/pull/9" || !run.DiffStored {
		t.Errorf("unexpected completed run: %+v", run)
	}
	if run.VerifyPass == nil || !*run.VerifyPass {
		t.Errorf("expected verify_pass true, got %v", run.VerifyPass)
	}
	if run.EndTime == nil {
		t.Errorf("expected end_time to be set")
	}
}

func TestCLITaskCompleteOptionalFields(t *testing.T) {
	tmpDir := setupTestEnv(t)
	id := startTestTaskRun(t)

	if _, errOut, err := runCLIStdout(t, "task", "complete", "--run-id", id, "--outcome", "timeout"); err != nil {
		t.Fatalf("task complete failed: %v\nstderr: %s", err, errOut)
	}

	db := setupTestDB(t, tmpDir)
	defer db.Close()
	run, err := storage.GetTaskRun(db, id)
	if err != nil {
		t.Fatal(err)
	}
	if run.Outcome != "timeout" || run.VerifyPass != nil || run.DiffStored || run.FinalCommit != "" {
		t.Errorf("unexpected run: %+v", run)
	}
}

func TestCLITaskValidation(t *testing.T) {
	setupTestEnv(t)
	id := startTestTaskRun(t)

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"start missing flags", []string{"task", "start", "--task-id", "1"}, "missing required flags"},
		{"complete missing run id", []string{"task", "complete", "--outcome", "complete"}, "--run-id"},
		{"complete bad outcome", []string{"task", "complete", "--run-id", id, "--outcome", "done"}, "--outcome must be one of"},
		{"complete bad verify", []string{"task", "complete", "--run-id", id, "--outcome", "complete", "--verify-pass", "maybe"}, "--verify-pass"},
		{"complete unknown run", []string{"task", "complete", "--run-id", "nope", "--outcome", "complete"}, "not found"},
		{"unknown subcommand", []string{"task", "bogus"}, "Unknown task command"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, errOut, err := runCLIStdout(t, tc.args...)
			if err == nil {
				t.Fatalf("expected failure")
			}
			if !strings.Contains(errOut, tc.want) {
				t.Errorf("stderr %q does not contain %q", errOut, tc.want)
			}
		})
	}
}

// TestHookScriptTaskRunID runs the real PreToolUse/PostToolUse hook scripts
// through a running daemon and checks task_run_id propagation.
func TestHookScriptTaskRunID(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}

	tmpDir := setupTestEnv(t)
	db := setupTestDB(t, tmpDir)
	defer db.Close()

	if _, err := runCLI(t, "daemon", "start"); err != nil {
		t.Fatalf("Failed to start daemon: %v", err)
	}
	defer func() {
		runCLI(t, "daemon", "stop") //nolint:errcheck
		time.Sleep(200 * time.Millisecond)
		os.Remove(filepath.Join(tmpDir, "daemon.sock")) //nolint:errcheck
	}()
	waitForDaemonReady(t, tmpDir)

	runID := startTestTaskRun(t)

	// Pre-create the session so prompt events don't race the daemon's
	// session_start handling (which arrives on a separate connection).
	createTestSession(t, db, "ses-task-run")

	scripts := map[string]string{
		"pre":  claudeToolPreTemplate,
		"post": claudeToolPostTemplate,
	}
	for name, tmpl := range scripts {
		path := filepath.Join(tmpDir, name+".py")
		content := strings.ReplaceAll(tmpl, "{{PROV_PATH}}", testBinary)
		if err := os.WriteFile(path, []byte(content), 0755); err != nil {
			t.Fatal(err)
		}
		scripts[name] = path
	}

	runHook := func(script, toolUseID, taskRunEnv string) {
		t.Helper()
		input, _ := json.Marshal(map[string]interface{}{
			"session_id":  "ses-task-run",
			"tool_name":   "Bash",
			"tool_use_id": toolUseID,
			"tool_input":  map[string]interface{}{"command": "echo " + toolUseID},
			"cwd":         tmpDir,
		})
		cmd := exec.Command("python3", script)
		cmd.Stdin = bytes.NewReader(input)
		env := []string{}
		for _, kv := range os.Environ() {
			if !strings.HasPrefix(kv, "AI_PROVENANCE_TASK_RUN_ID=") {
				env = append(env, kv)
			}
		}
		if taskRunEnv != "" {
			env = append(env, "AI_PROVENANCE_TASK_RUN_ID="+taskRunEnv)
		}
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("hook script failed: %v\n%s", err, out)
		}
	}

	waitTaskRunID := func(marker string) sql.NullString {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			var id sql.NullString
			err := db.QueryRow(`SELECT task_run_id FROM prompt_events WHERE prompt_text LIKE ?`,
				"%"+marker+"%").Scan(&id)
			if err == nil {
				return id
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("event %s never stored", marker)
		return sql.NullString{}
	}

	runHook(scripts["pre"], "with-pre", runID)
	runHook(scripts["post"], "with-post", runID)
	runHook(scripts["pre"], "without-pre", "")

	for _, marker := range []string{"with-pre", "with-post"} {
		if id := waitTaskRunID(marker); !id.Valid || id.String != runID {
			t.Errorf("%s: expected task_run_id %q, got %+v", marker, runID, id)
		}
	}
	if id := waitTaskRunID("without-pre"); id.Valid {
		t.Errorf("expected NULL task_run_id when env var absent, got %q", id.String)
	}
}

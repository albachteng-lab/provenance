package main

import (
	"database/sql"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/albachteng/provenance/internal/storage"
)

// validTaskOutcomes lists the outcomes accepted by `prov task complete`.
var validTaskOutcomes = []string{"complete", "partial", "blocked", "needs-info", "escalated", "timeout"}

const taskUsage = "Usage: prov task <start|complete> [flags]"

func cmdTask() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, taskUsage)
		os.Exit(1)
	}

	var err error
	switch os.Args[2] {
	case "start":
		err = taskStart(os.Args[3:], os.Stdout, os.Stderr)
	case "complete":
		err = taskComplete(os.Args[3:], os.Stderr)
	default:
		fmt.Fprintf(os.Stderr, "Unknown task command: %s\n", os.Args[2])
		fmt.Fprintln(os.Stderr, taskUsage)
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// openTaskDB opens (creating if needed) the SQLite database directly so that
// the runner can record task runs even when the daemon is not running.
func openTaskDB() (*sql.DB, error) {
	return storage.InitDatabase(getDBPath())
}

// taskStart creates a task_runs row and prints its UUID to out.
func taskStart(args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("task start", flag.ContinueOnError)
	fs.SetOutput(errOut)
	taskID := fs.String("task-id", "", "GitHub issue number of the task (required)")
	repo := fs.String("repo", "", "Repository as owner/repo (required)")
	agent := fs.String("agent", "", "Agent running the task, e.g. claude (required)")
	harnessHash := fs.String("harness-hash", "", "Git SHA of the runner harness (required)")
	baseCommit := fs.String("base-commit", "", "Git SHA the task started from")
	retryCount := fs.Int("retry-count", 0, "Retry attempt number")
	if err := fs.Parse(args); err != nil {
		return err
	}

	var missing []string
	for name, v := range map[string]string{
		"--task-id": *taskID, "--repo": *repo, "--agent": *agent, "--harness-hash": *harnessHash,
	} {
		if v == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		return fmt.Errorf("missing required flags: %s", strings.Join(missing, ", "))
	}
	if *retryCount < 0 {
		return fmt.Errorf("--retry-count must be >= 0")
	}

	db, err := openTaskDB()
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck

	run := &storage.TaskRun{
		TaskID:         *taskID,
		Repo:           *repo,
		Agent:          *agent,
		HarnessGitHash: *harnessHash,
		BaseCommit:     *baseCommit,
		RetryCount:     *retryCount,
	}
	if err := storage.CreateTaskRun(db, run); err != nil {
		return err
	}

	fmt.Fprintln(out, run.ID)
	return nil
}

// taskComplete records the outcome of a task run.
func taskComplete(args []string, errOut io.Writer) error {
	fs := flag.NewFlagSet("task complete", flag.ContinueOnError)
	fs.SetOutput(errOut)
	runID := fs.String("run-id", "", "UUID returned by 'prov task start' (required)")
	outcome := fs.String("outcome", "", "Outcome: "+strings.Join(validTaskOutcomes, "|")+" (required)")
	finalCommit := fs.String("final-commit", "", "Final git SHA (empty if no commits)")
	verifyPass := fs.String("verify-pass", "", "Whether verification passed: true|false")
	verifySummary := fs.String("verify-summary", "", "Verification summary as inline JSON")
	prURL := fs.String("pr-url", "", "Pull request URL")
	diffStored := fs.String("diff-stored", "false", "Whether the diff was stored: true|false")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *runID == "" {
		return fmt.Errorf("missing required flag: --run-id")
	}
	if !slices.Contains(validTaskOutcomes, *outcome) {
		return fmt.Errorf("--outcome must be one of: %s", strings.Join(validTaskOutcomes, ", "))
	}

	completion := storage.TaskRunCompletion{
		Outcome:       *outcome,
		FinalCommit:   *finalCommit,
		VerifySummary: *verifySummary,
		PRURL:         *prURL,
	}
	if *diffStored != "" {
		v, err := strconv.ParseBool(*diffStored)
		if err != nil {
			return fmt.Errorf("--diff-stored must be true or false: %w", err)
		}
		completion.DiffStored = v
	}
	if *verifyPass != "" {
		v, err := strconv.ParseBool(*verifyPass)
		if err != nil {
			return fmt.Errorf("--verify-pass must be true or false: %w", err)
		}
		completion.VerifyPass = &v
	}

	db, err := openTaskDB()
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck

	if err := storage.CompleteTaskRunWith(db, *runID, completion); err != nil {
		if err == storage.ErrNotFound {
			return fmt.Errorf("task run %s not found", *runID)
		}
		return err
	}
	return nil
}

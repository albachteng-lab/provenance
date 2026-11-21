# AGENTS.md — Working Guide for Provenance

Provenance records AI prompt events from editor/CLI hooks and correlates them with git
commits. This file is the agent-facing map: where things live, what must not break, and
how to test. For user-facing docs see `README.md`.

## Stack and entry points

- **Language:** Go (see `go.mod` for the version).
- **SQLite driver:** `github.com/mattn/go-sqlite3` — this is a **CGO** driver, so building
  and testing need a C toolchain (`CGO_ENABLED=1`, which is the default when gcc is present).
- **Migrations:** `github.com/golang-migrate/migrate/v4`, with SQL files embedded via
  `//go:embed all:migrations` in `internal/storage/migrations.go`.
- **Config:** YAML (`gopkg.in/yaml.v3`). Global config lives in `$AI_PROVENANCE_HOME/config.yaml`;
  a per-repo override can live in `<repo>/.ai-provenance/config.yaml`.
- **Single binary:** `cmd/prov`. It is both the daemon (`prov daemon start|stop|status`)
  and the CLI (`list`, `show`, `search`, `session`, `stats`, `export`, `blame`, `tag`/`untag`,
  `capture-hook`, `correlate-commit`, `install-hooks`, `install-hook`, ...). Subcommand dispatch is in `cmd/prov/main.go`.
- **Daemon IPC:** Unix socket at `~/.ai-provenance/daemon.sock`.
- **Database:** `~/.ai-provenance/db.sqlite`, opened in WAL mode (`storage.InitDatabase`).
- **Home override:** set the `AI_PROVENANCE_HOME` environment variable to move the socket, DB,
  and global config somewhere else (`getProvenanceHome` in `cmd/prov/main.go`). Tests rely on this.

Build: `go build -o prov ./cmd/prov`

## Package map

| Package | Responsibility |
|---|---|
| `cmd/prov/` | CLI entry point (`main.go`), all subcommands (`commands.go`), embedded hook templates (`hooks/`) |
| `internal/storage/` | SQLite init (WAL), embedded migrations, read/write functions for events, sessions, change sets |
| `internal/daemon/` | Unix socket server, event decoding and dispatch to storage and session manager |
| `internal/session/` | Session boundary detection strategies (smart-time, git-event, git polling) and `Manager` |
| `internal/correlation/` | Prompt-to-commit correlation (links commits to prompt events as change sets) |
| `internal/git/` | Git state reads (commit, branch, dirty files) behind a `Provider` interface (`cli_provider.go`) |
| `internal/config/` | Config defaults, layered loading (global + repo), session strategy selection |

## Testing

```bash
go test ./...          # full suite
go test -race ./...    # must pass; CI runs this (.github/workflows/ci.yml)
gofmt -l . && go vet ./...
```

CI also runs `golangci-lint run ./...`.

- No external services and no pre-running daemon are needed. Note that some `cmd/prov`
  tests (`hooks_test.go`, `cli_test.go`, `git_hook_test.go`) build the binary and start a
  **real daemon subprocess** pointed at a temp `AI_PROVENANCE_HOME`. Some tests are known to be
  flaky under `-race` when the whole suite runs: `TestCaptureHookUserPrompt` and
  `TestCaptureHookToolUse` in `cmd/prov` (event not found within timeout), and occasionally
  `TestDaemonStartAndBind`. They pass when run alone. Rerun them before you assume your change
  broke them.
- Put every SQLite file, socket, and config file in a temp directory (`t.TempDir()` for new
  tests; some older tests use `os.MkdirTemp` + `t.Cleanup`). **Never touch the user's real
  `~/.ai-provenance/`.** When a test runs the CLI, set `AI_PROVENANCE_HOME` to the temp dir.
- For new server-side tests, follow `internal/daemon/server_test.go`. Its `setupTestDaemon`
  creates a temp DB via `storage.InitDatabase`, sets the socket path from `config.Default()`
  to a temp location, and polls the DB with `waitForEvent` instead of sleeping.

## Invariants

- **`prompt_events` is append-only.** Insert through `storage.StorePromptEvent`; never add
  code that runs `UPDATE` or `DELETE` on existing rows. Derived data belongs in other tables
  (`sessions` metrics, `change_sets`).
- **Migrations are immutable.** They live in `internal/storage/migrations/` as
  `NNNNNN_name.up.sql` / `.down.sql` pairs. Always add a new, higher-numbered pair; never
  edit a migration that has already shipped, because installed databases will not re-run it.
- **Database access belongs in `internal/storage/`.** Add new queries there as functions and
  call them from other packages. Some older code runs SQL directly: queries in
  `cmd/prov/commands.go` and `internal/correlation/correlation.go`. Do not add more of it, and
  move those queries into `storage` when you touch them.
- **Hook templates are embedded.** The files in `cmd/prov/hooks/` (`claude-*.py`, `post-commit.sh`)
  are compiled in via `//go:embed` in `cmd/prov/commands.go`. After you change one, rebuild the
  binary and reinstall the hooks (`prov install-hooks` for Claude Code, `prov install-hook` for the git post-commit hook) before the change takes effect.
- **Hooks must not break the host tool.** `capture-hook` and the hook scripts fail silently
  (exit 0) so that Claude Code or git never blocks because Provenance is down.
- **Redact secrets before you persist tool inputs or outputs.** Patterns belong in the
  `redaction_rules` table, plus `redaction` in config (`builtin_patterns`, `custom_patterns`).
  As of this writing, the table and config exist but no redaction runs on the write path. Any
  new code that stores tool input/output must redact it. Do not assume the existing write path
  already does.

## Important docs

- `README.md` — user-facing usage, configuration, troubleshooting, project structure.
- `ROADMAP.md` — phase status (what is ✅ complete versus planned) and resolved design decisions.
- `DESIGN.md` — MVP design: data model, integration patterns, git correlation strategy.
- `TESTING.md` — manual end-to-end test walkthrough using the real binary.
- `CHANGELOG.md` — release history.
- `internal/storage/migrations/` — the authoritative schema history.

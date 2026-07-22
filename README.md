# foreman-runner

Agent runner for [foreman](https://github.com/mluex/foreman). Single Go binary,
standard library only.

It enrolls a machine with a foreman server, then runs a daemon that reports
health, polls for tasks, verifies each task's signature, launches the coding
agent (Claude Code) inside a `tmux` session, and streams the live output back to
the server.

## Build

```sh
go build -o foreman-runner .
```

Requires Go 1.22+. `tmux` and `claude` must be on `PATH` at run time; the binary
fails loudly if either is missing.

## Quickstart

Enroll the machine with a code from the foreman web UI, then start the daemon:

```sh
./foreman-runner enroll --code=ABCD-EFGH-IJ --server=https://foreman.example.com
./foreman-runner run --dir /path/to/workspace
```

`enroll` generates an Ed25519 keypair, exchanges the code for an API token, and
writes `~/.config/foreman/runner.json` (mode 0600). `run` then heartbeats and
polls for work.

## Commands

### `enroll`

Registers the machine. Detects OS/arch, generates the keypair, and persists the
config.

| Flag         | Default              | Notes                                        |
| ------------ | -------------------- | -------------------------------------------- |
| `--code`     | –                    | Enrollment code from the web UI (required)   |
| `--server`   | –                    | foreman base URL (required)                  |
| `--name`     | hostname             | Runner name hint                             |
| `--config`   | XDG config path      | Config file location                         |
| `--insecure` | `false`              | Skip TLS verification (self-signed dev only) |
| `--force`    | `false`              | Overwrite an existing config                 |

### `run`

The daemon. Sends a signed heartbeat every `--interval`, polls
`--poll-interval` for a task, and runs one task at a time.

| Flag              | Default   | Notes                                          |
| ----------------- | --------- | ---------------------------------------------- |
| `--dir`           | cwd       | Working directory agents run in                |
| `--interval`      | `30s`     | Heartbeat interval                             |
| `--poll-interval` | `5s`      | Task poll interval                             |
| `--claude-bin`    | `claude`  | Agent binary name or path                      |
| `--once`          | `false`   | Send a single heartbeat and exit               |
| `--insecure`      | `false`   | Skip TLS verification (self-signed dev only)   |
| `--config`        | XDG path  | Config file location                           |

### `spawn`

A standalone launcher for the tmux/agent mechanism, useful for testing a machine
before enrolling it. It takes a directory and a prompt and starts an interactive
Claude Code session with log capture. Run `foreman-runner spawn -h` for flags.

## How a task runs

1. `run` polls `GET /api/runners/{id}/next-task` (bearer auth) and claims one
   pending task.
2. It verifies the task's Ed25519 signature against the owner's public key over
   the exact stored payload bytes. On failure it posts `reject` and keeps
   polling.
3. It starts a detached `tmux` session `foreman-task-<uuid>`, arms
   `pipe-pane` log capture, then launches the agent.
4. A background tailer batches new log bytes every ~2s and posts them to
   `/api/tasks/{id}/logs` with a per-task monotonic `seq` (the server dedups on
   `(task, seq)`).
5. When the agent exits, the runner flushes the remaining log output, then posts
   `/api/tasks/{id}/finish` with the exit code.

Heartbeat, reject, finish, and log requests are signed with the runner's Ed25519
key and carry a timestamp; the server enforces a 60s freshness window.

## Interactive launch gates (verified end-to-end)

An interactive Claude Code launch has gates that each wait for keyboard input
and would stall an unattended runner. What actually happens, tested against
`claude` v2.1.216:

1. **Workspace trust dialog** — "Is this a project you trust?", shown the first
   time claude opens a directory. Stored per directory in `~/.claude.json` under
   `projects.<dir>.hasTrustDialogAccepted`. The runner pre-accepts it for the
   working directory before launch.
2. **`--dangerously-skip-permissions` is a trap here.** It does **not** skip the
   trust dialog, and it adds its *own* one-time "Bypass Permissions mode"
   acceptance dialog that also waits for input. So it makes unattended launches
   worse, not better.
3. **`--permission-mode` is the answer.** With trust pre-seeded and a
   non-interactive permission mode, claude boots straight into a live session
   with no gate. `/remote-control` then activates and prints a
   `claude.ai/code/session_...` link — the run is controllable from Claude Web
   and mobile.

The launch is interactive on purpose (not `claude -p`): `/remote-control` needs
a live, running session, whereas `-p` prints and exits. This matches the foreman
trust model: runners execute with the trust of the operator who deployed them.

## Config file

`~/.config/foreman/runner.json` (honoring `XDG_CONFIG_HOME`), mode 0600:

```json
{
  "runner_id": "...",
  "runner_privkey": "base64 Ed25519 private key",
  "api_token": "...",
  "user_pubkey": "base64 Ed25519 public key",
  "server_url": "https://foreman.example.com",
  "os": "linux",
  "arch": "amd64"
}
```

The owner public key is refreshed from each heartbeat response, so operator key
rotations are picked up automatically.

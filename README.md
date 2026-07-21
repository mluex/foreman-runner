# foreman-runner

Agent runner for [foreman](https://github.com/mluex/foreman). Single Go binary,
standard library only.

## Status: proof of concept

This build validates the core mechanism only. It has no enrollment, polling,
signature verification or log streaming yet — those come with the foreman
milestones. What it does:

1. Take a target directory and a prompt.
2. Spawn a detached `tmux` session in that directory.
3. Launch Claude Code there interactively, in `auto` permission mode.
4. Submit the prompt, prefixed with `/remote-control` so the live session is
   controllable from Claude Web.
5. Capture the full session output to a log file via `tmux pipe-pane`.

Interactive (not `-p`) is deliberate: `/remote-control` needs a live, running
session; `-p` prints and exits.

## Build

```sh
go build -o foreman-runner .
```

## Run

```sh
./foreman-runner --dir /path/to/repo --prompt "Fix the failing test in foo_test.go"
```

Useful flags:

| Flag                | Default   | Notes                                              |
| ------------------- | --------- | -------------------------------------------------- |
| `--dir`             | –         | Working directory (required)                       |
| `--prompt`          | –         | Prompt submitted to the agent (required)           |
| `--remote-control`  | `true`    | Prefix the prompt with `/remote-control`           |
| `--permission-mode` | `auto`    | Passed to `claude --permission-mode`               |
| `--model`           | –         | Passed to `claude --model` (empty = claude default)|
| `--trust`           | `true`    | Pre-accept the workspace trust dialog for `--dir`  |
| `--skip-permissions`| `false`   | Add `--dangerously-skip-permissions` (see below)   |
| `--claude-arg`      | –         | Extra arg passed to claude verbatim (repeatable)   |
| `--session`         | generated | tmux session name                                  |
| `--log-dir`         | XDG state | Where the session log is written                   |
| `--attach`          | `false`   | Attach to the tmux session after launch            |

After launch the binary prints the session name, log path, and the
`tmux attach` / `tail` / `kill` commands.

## Interactive launch gates (verified end-to-end)

An interactive Claude Code launch has gates that each wait for keyboard input
and would stall an unattended runner. What actually happens, tested against
`claude` v2.1.216:

1. **Workspace trust dialog** — "Is this a project you trust?", shown the first
   time claude opens a directory. Stored per directory in `~/.claude.json` under
   `projects.<dir>.hasTrustDialogAccepted`. The runner pre-accepts it for
   `--dir` before launch (`--trust`, on by default).
2. **`--dangerously-skip-permissions` is a trap here.** It does **not** skip the
   trust dialog, and it adds its *own* one-time "Bypass Permissions mode"
   acceptance dialog that also waits for input. So it makes unattended launches
   worse, not better. It is off by default (`--skip-permissions=false`).
3. **`--permission-mode auto` is the answer.** With trust pre-seeded and auto
   mode, claude boots straight into a live session with no gate; in-session
   permission decisions are handled by auto mode's classifier. `/remote-control`
   then activates and prints a `claude.ai/code/session_...` link — the run is
   controllable from Claude Web and mobile.

This matches the foreman trust model: runners execute with the trust of the
operator who deployed them.

## Requirements

`tmux` and `claude` must be on `PATH`; the binary fails loudly if either is
missing.

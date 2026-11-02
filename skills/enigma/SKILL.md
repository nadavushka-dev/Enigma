---
name: enigma
description: Use when the user wants to keep files or folders identical across their machines with Enigma: syncing dotfiles, agent rules, or notes; adding a path that already exists on another machine; checking why a file is not syncing; resolving conflict copies; restoring a deleted file; managing Enigma groups or machines; or changing fleet limits. Trigger on "sync my dotfiles", "get ~/.claude onto devbox", "why isn't ~/notes on nas", "only share this folder with laptop and devbox", "enigma status", "enigma doctor", or "enigma conflicts".
---

# Enigma

Enigma keeps chosen files and folders under `~` identical on every machine in
a fleet, live, over the system `ssh`. Each machine runs a daemon; changes are
announced over links. Deletes go to a per-machine trash, and concurrent edits
are merged or kept as conflict copies.

Use the `enigma` CLI. If `command -v enigma` fails, look at
`~/.local/bin/enigma`; do not install anything without asking.

## Before you act

- Read the fleet before changing it:
  - `enigma status --json`
  - `enigma group list --json`
  - `enigma machine list --json`
- `status --json` puts standing problems first in `problems`; show those
  before anything else.
- Never edit `~/.local/share/enigma/state.json`, `identity.json`, the history
  folder, or the daemon sockets.
- Use `--json` for reads; do not scrape the human tables.
- If the daemon is not running, `status` says so. Ask before
  `enigma service install` (it changes login items); `enigma daemon` runs it
  in the foreground.

## Add a path safely (D22)

`enigma add` has a first-add preview. It asks the group members what they
have at the path and compares. A path may exist on another machine with
different content; the choice decides whether that content is kept or
replaced.

1. Preview:
   ```sh
   enigma add --dry-run --json ~/path
   ```
2. Read the report:
   - `report.summary.different` — files that exist here and elsewhere with
     different content.
   - `report.summary.only_there` — files only on other machines; kept and
     spread.
   - `report.summary.unknown` — machines offline; the choice applies when
     they reconnect.
   - `report.files` — the differing files and the machines they differ on.
   - `report.machines` — every member, whether it has the path, and whether
     it is connected.
3. Choose:
   - No differences → `enigma add --json ~/path`.
   - Differences and the user wants both copies (or has no preference) →
     `enigma add --on-existing=keep-both --json ~/path`. This is the default
     and the safe choice: the other machine's copy stays visible as
     `<name>.conflict-<machine>-<time>-<hash>`.
   - The user explicitly says "replace theirs" →
     `enigma add --on-existing=mine-wins --json ~/path`. The other copy is
     backed up on that machine and replaced.
   - The user has not decided → show the report and ask. Never choose
     `mine-wins` silently.
   - Cancel → do not add.
4. Verify:
   ```sh
   enigma status --json
   ```
   For a folder, a first-add copy syncs to every member. For a single file,
   the losing copy exists only on the machines that resolved the conflict
   (the adder and the machine that lost); the canonical file syncs
   everywhere.
5. If the path is already synced, `add` fails. Use
   `enigma entry move ~/path <group>` to change its group, or
   `enigma remove ~/path` to stop syncing (files stay). Ask first.

A difference with no `--on-existing` and no terminal **fails closed** with an
error naming the flag. Agent shells have no terminal, so pass the flag
explicitly.

## Diagnose

Start with:

```sh
enigma status --json
```

`problems` is the standing set, errors first. Each has a `message` and a
`hint`; quote both. Then:

- `enigma doctor --json` — connectivity matrix, versions, service state;
  exits non-zero on problems.
- `enigma conflicts --json` — conflict copies with `path`, `original`,
  `machine`, `on`. Use `enigma conflicts --local` on one machine if you only
  need local copies.
- The daemon log is `~/.local/share/enigma/enigma.log` (the data folder is
  under `ENIGMA_HOME` when set). Read the last lines with
  `tail -n 200 ~/.local/share/enigma/enigma.log`.

Common causes:

- **Link down:** `peers[].last_error` in `status`; check
  `ssh <alias> true`, the alias, and `enigma doctor`.
- **Daemon down:** `enigma service install` (ask first) or `enigma daemon`.
- **Over the file limit:** `entries[].OverLimit` (the nested entry keys are
  capitalized: `Path`, `Dir`, `Group`, `Frozen`, `Missing`, `Skipped`,
  `OverLimit`). Raise with `enigma limits --files N` after asking.
- **Skipped files:** `entries[].Skipped` — symlinks or over the size limit.
- **Entry root vanished:** `entries[].Missing`; deletes are suppressed until
  it is back.
- **Version mismatch:** the fleet upgrades itself; wait for the link to come
  back. Do not run a dev build against a live fleet.
- **Conflict:** compare the original and the copy; keep what is needed, then
  delete the copy. Do not delete either version before deciding.

## Groups, machines, limits

- `enigma group list --json`, `group create`, `group add`, `group remove`,
  `group rename`, `group merge`, `group delete`.
- `enigma entry move ~/path <group>`.
- `enigma machine add <ssh-alias>`, `machine list --json`, `machine remove`,
  `machine rename`, `machine alias`.
- `enigma limits --json`, `enigma limits --file-size 25MB --files 5000`.

Membership changes and limits affect every machine. Show the plan and
confirm before applying. `group delete` and `group merge` stop syncing
entries; the files stay.

## Destructive actions

Ask before any of these, and show exactly what changes:

- `enigma remove <path>` — stops syncing; files stay.
- `enigma group delete <name>` / `group merge` with `--yes` — entries stop
  syncing; files stay.
- `enigma machine remove <name> [--uninstall]` — the machine leaves;
  `--uninstall` also removes its service and binary.
- `enigma trash empty` — permanently empties this machine's trash.
- `enigma trash restore <id>` — restores locally and syncs as an edit.
  Restore on one machine, then verify; restoring on several can conflict.

`enigma trash list --json` shows `id`, `path`, `day`, `size`, `mtime`.

## Do not

- Do not edit Enigma's state, history, sockets, or daemon log.
- Do not use `--on-existing=mine-wins` unless the user explicitly chose to
  replace.
- Do not pass `--yes` to a destructive command without showing the plan
  first.
- Do not run a dev build (`make build`) against a live fleet: a newer binary
  upgrades every machine over its links.
- Do not sync outside `~`, symlinks, `.git`, `node_modules`, or editor swap
  files.
- Do not kill the daemon or delete its sockets to "fix" a link; use
  `status`, `doctor`, and the log.

## References

- `references/commands.md` — every CLI command and flag.
- `references/json.md` — the JSON fields used above.
- `references/safety.md` — destructive actions, recovery, and conflict
  cleanup.

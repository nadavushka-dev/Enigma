# Enigma CLI

Global:

- `enigma -h`, `enigma --help` — every command.
- `enigma <command> -h` — one command. `enigma help <command>` also works.
- `enigma -v`, `enigma --version` — version and platform.
- `--json` is available on the commands marked below. `enigma list` is human
  only; use `enigma status --json` for entries in scripts.

## Setup and machines

| Command | Does |
|---|---|
| `enigma init [--name NAME]` | Create a fleet with this machine as its first member. |
| `enigma machine add <ssh-alias> [--name NAME] [--group G]…` | Install, join and link a machine over ssh. `--group` defaults to `main`. |
| `enigma machine list [--json]` | Members, their link and what this machine dials. |
| `enigma machine remove <name> [--uninstall]` | Remove a machine; `--uninstall` also removes its service and binary. |
| `enigma machine rename <name> <new>` | Rename a machine. |
| `enigma machine alias <name> [<ssh-alias>]` | Show or set this machine's local ssh alias for another. |
| `enigma service install` / `uninstall` | Run the daemon at login (launchd / systemd). |
| `enigma daemon [-v] [--log PATH]` | Run the daemon in the foreground; `-v` is debug logging. |
| `enigma update [--check]` | Move this machine to the latest release; the fleet follows. |
| `enigma version`, `enigma -v`, `enigma --version` | Version and platform. |
| `enigma tui` | The terminal UI (bare `enigma` in a terminal). |

## Entries

| Command | Does |
|---|---|
| `enigma add [--group G] [--ignore GLOB]… [--on-existing keep-both\|mine-wins\|cancel] [--dry-run] [--json] [path]` | Sync a file or folder. `--dry-run` shows the first-add report; `--on-existing` answers it; no path opens the `fzf` picker. |
| `enigma remove <path>` | Stop syncing; the files stay everywhere. |
| `enigma list` | Human list of synced entries and groups. Use `status --json` in scripts. |
| `enigma status [--json]` | Problems first, then peers, entries, limits and conflict copies. |
| `enigma entry move <path> <group>` | Move an entry to another group. |

## Groups

| Command | Does |
|---|---|
| `enigma group list [--json]` | Groups, members and entry counts. |
| `enigma group create <name> [machine]…` | Create a group; no machine means this one only. |
| `enigma group delete <name> [--yes]` | Stop syncing its entries; the files stay. Shows the plan and asks. |
| `enigma group rename <name> <new>` | Rename. |
| `enigma group add <name> <machine>…` / `remove <name> <machine>…` | Change membership; entering/leaving runs the first-add rules. |
| `enigma group merge <from> <into> [--yes] [--add-members]` | Move every entry, then delete `<from>`; shows the plan and asks. |

## Diagnosis and safety

| Command | Does |
|---|---|
| `enigma doctor [--json]` | Connectivity matrix, versions and service state over ssh; non-zero on problems. |
| `enigma conflicts [--local] [--json]` | Conflict copies, their original and the machines that hold them. |
| `enigma limits [--file-size SIZE] [--files N] [--json]` | Show or set the fleet limits (`25MB`, `5000`). |
| `enigma trash list [--json]` | Files deleted here by a synced delete. |
| `enigma trash restore <id>` | Restore one; it syncs as a normal edit. |
| `enigma trash empty` | Permanently empty this machine's trash. |

## First-add choices

When `enigma add` finds a path that already exists on another machine:

| Choice | What happens |
|---|---|
| `keep-both` (default) | The adder's copy stays at the path; each other machine's differing copy becomes a visible `<name>.conflict-<machine>-<time>-<hash>` file. Folder copies sync everywhere; a single-file copy stays on the machines that resolved the conflict. |
| `mine-wins` | The adder's copy stays; each other machine's differing copy is backed up locally and replaced. |
| `cancel` | Nothing is added. |

The choice is stored on the entry, so machines that were offline follow it
when they reconnect. The CLI is non-interactive when there is no terminal;
without `--on-existing` a difference cancels with an error naming the flag.

## Limits and what syncs

- Files and folders under `~`; only regular files. Symlinks are listed as
  skipped.
- Defaults: 10 MB per file, 2,000 files per folder. Ceilings: 64 MB and
  20,000.
- Always ignored: `.git/`, `node_modules/`, `.DS_Store`, editor swap files.
  Add your own with `--ignore`.
- A folder over its file limit keeps syncing what it has; new files wait and
  `status` shows them in `OverLimit`.

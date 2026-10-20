# Enigma — design spec

Status: **agreed**, 2026-10-20. Nothing is implemented yet.
Sections 1–3 record what was decided in discussion. Sections 4 onward are
the technical design. Section 14 records how the remaining open points were
resolved.

---

## 1. Problem and purpose

Agentic work burns a lot of local resources, so work gets spread over several
machines. Agent-facing files then drift apart: improve `~/.claude/CLAUDE.md`
on machine A and machine B keeps the old one, so B's agents do worse work.

Enigma keeps a chosen set of files and folders identical on all your
machines, in real time, over ssh.

**What it is not:**
- Not a Git replacement. It is not meant for projects or code bases, and the
  size limits (§8) make that impractical on purpose.
- Not a shared network drive. Every machine works on its own local copy.
- Not related to any other tool. It stands alone and talks to other machines
  only through the standard `ssh` command.

## 2. Decisions so far

| # | Topic | Decision |
|---|---|---|
| D1 | Network | Every machine can reach every other machine over ssh. |
| D2 | Model | Every machine keeps a local copy. A local change is detected through OS file events and sent to the other machines. No locks. |
| D3 | Topology | Direct to all. Every machine runs the same daemon; there is no hub. |
| D4 | Conflicts | Edits to different lines are merged automatically. Overlapping edits: the newer edit wins in place, and the other is kept as a conflict copy that syncs like any file. Binary files skip the merge. |
| D5 | Machines | One group containing all your machines, picked from your ssh config. No per-file machine lists and no exclusions; groups may come later. |
| D6 | What syncs | Files and folders, at the same path relative to `~` on every machine. Created on machines that don't have it yet, including parent folders. |
| D7 | First add | The machine where you add a file or folder wins. Machines that already have a different copy back it up first. |
| D8 | Deletes | Spread to all machines. Each machine first moves its copy to a local trash, kept 30 days. An edit beats a concurrent delete. |
| D9 | Setup | Adding a machine installs Enigma on it over ssh (binary + background service) and shows each step. |
| D10 | Interface | A CLI for everything (scriptable, usable by agents) plus a TUI for picking machines and files and watching status. |
| D11 | Alerts | `enigma status` and the TUI show everything. Conflicts also raise a desktop notification. |
| D12 | Dev tools | tuxedo, tuicr and herdr are for our development workflow only, never part of Enigma. |
| D13 | Backups | First-add backups are stored locally on the machine that made them, never next to the file. |
| D14 | Folder first add | Files that exist only on another machine are kept and spread to everyone. Files that differ follow D7. |
| D15 | Symlinks | Skipped and listed in status. |
| D16 | Paths | Only paths under `~`. |
| D17 | Repo | `github.com/nadavushka-dev/enigma`; releases come from there. |
| D18 | Limits | 10 MB per file, 2,000 files per folder entry. |

## 3. Vocabulary

- **Machine:** a computer in the group, running the Enigma daemon.
- **Group:** the set of machines plus the set of entries. There is exactly one.
- **Entry:** a file or folder you chose to sync, stored as a path relative to
  `~` (e.g. `.claude/CLAUDE.md`, `.claude/skills/`).
- **Tracked file:** a regular file that is synced, either an entry itself or
  a file inside a folder entry.
- **Version:** one state of a tracked file: content hash (or "deleted"),
  version vector, parents, mtime, exec bit.
- **Peer link:** the single ssh connection between two machines.

---

## 4. Architecture

```
            machine A                                   machine B
 ┌─────────────────────────────┐             ┌─────────────────────────────┐
 │ enigma CLI / TUI            │             │ enigma CLI / TUI            │
 │        │ unix socket        │             │        │                    │
 │ enigma daemon               │  ssh -T B   │ enigma daemon               │
 │  ├ watcher (fsnotify)       │────────────▶│  ▲                          │
 │  ├ engine (versions, merge) │  "enigma    │  │ unix socket              │
 │  ├ group state              │   bridge"   │ enigma bridge (stdio ⇄ sock)│
 │  └ store (~/.local/share)   │◀────────────│                             │
 └─────────────────────────────┘  one link,  └─────────────────────────────┘
                                  both ways
```

- One static Go binary, `enigma`, with subcommands. It runs on macOS and
  Linux, amd64 and arm64.
- **daemon:** long-running, one per machine, started by launchd (macOS) or a
  systemd user unit (Linux). It owns all state on that machine.
- **bridge:** what a peer runs over ssh. It only copies bytes between ssh's
  stdin/stdout and the local daemon's unix socket. This lets two daemons
  talk to each other over plain ssh, with nothing listening on the network.
- **CLI/TUI:** clients of the local daemon's unix socket. They never touch
  the state files directly.

## 5. Connections between machines

- **ssh command:** `ssh -T -o BatchMode=yes -o ServerAliveInterval=15 -o ServerAliveCountMax=3 -o ControlMaster=no -o ControlPath=none <target> <remote-enigma> bridge`
  - Enigma uses the system `ssh` binary, so the user's ssh config, keys,
    agent and ProxyJump apply.
  - The link gets its own connection (`ControlPath=none`), so it never
    shares, or gets torn down with, a multiplexed session.
- **One link per pair.** Both machines try to dial each other. If the
  handshake finds a link for that pair already exists, the link dialed by
  the machine with the lower machine ID is kept and the other is closed.
  This keeps a single link and still works when ssh only works in one
  direction.
- **Reaching a peer** (target resolution on machine X for peer P):
  1. a local override set on X: `enigma machine alias P <alias>`;
  2. P's recorded ssh alias, if X has the same alias;
  3. P's recorded `user@hostname:port`, captured with `ssh -G` on the
     machine that added P.

  The first that connects is remembered.
- **Heartbeat:** each side pings every 15 s and drops a link that has been
  silent for 45 s. ssh keepalive protects only the dialing side; without
  this, the accepting side would keep a dead link "up" for many minutes.
- **Reconnect** with backoff of 1 s doubling up to 60 s. Every reconnect
  starts with a catch-up (§6.5).
- **Handshake:** `HELLO {group_id, machine_id, proto_version, enigma_version, platform}`.
  A different group or a removed machine closes the link with a clear
  error, shown in status. A different Enigma version turns the link into
  an upgrade (§11).

## 6. Sync model

### 6.1 Detecting local changes
- fsnotify watches every entry: a file entry's parent folder (filtered to the
  file), or a folder entry recursively, adding new subfolders as they appear.
- Events are debounced for 200 ms of quiet, capped at 2 s. Editors save in
  bursts (temp file, rename).
- After the debounce the file is hashed (SHA-256). If the hash equals the
  current version, nothing changed. This is also what stops echoes: when
  Enigma writes a received version, its own watcher fires and finds the same
  hash.
- A safety rescan runs at daemon start, after a watcher error, and every 10
  minutes.

### 6.2 Versions
Each tracked file has a current version with:
- `hash`: content SHA-256, or `deleted`
- `vector`: version vector `{machine_id: counter}`
- `parents`: hashes of the versions it was made from (1 normally, 2 after a merge)
- `mtime` and `exec`

A local change on machine X creates a new version: `vector[X] += 1`, with
the previous version as parent.

### 6.3 Spreading changes (announce, then fetch)
1. When a machine gets a new current version, whether made locally or
   received, it sends `ANNOUNCE {path, hash, vector, mtime}` on every link
   except the one it came from.
2. The receiver compares vectors:
   - incoming is older or equal: ignore it;
   - incoming is newer: send `FETCH`, receive `FILE {path, version, ancestry, content}`, apply it (§6.6);
   - neither is newer, i.e. concurrent edits: fetch it and resolve (§7).
3. Because receivers pass announcements on, a change reaches machine C
   through B when the A–C link is down. Announcing first means a file is
   never sent twice to the same machine.

Files are always sent whole. They are small by design (§8).

### 6.4 Group state
The machine list, the entries and group settings form the **group state**,
replicated to every machine.
- Each record (a machine, an entry) carries a Lamport timestamp and the ID of
  the machine that last changed it. The highest timestamp wins, with the
  machine ID as tie-break. Removed records stay behind as markers ("tombstones").
- On connect and on every change, machines exchange records the same way they
  exchange file versions.
- This is how adding an entry on any machine reaches all machines (D5, D6).

### 6.5 Catch-up after being offline
When a link comes up, the two machines exchange a summary
`{path: (hash, vector)}` of all tracked files. Each side then treats every
difference as an announcement (§6.3). Group state is exchanged first, so new
entries are known before files are compared.

**Changes made while the daemon was not running** are found by the startup
rescan, which compares disk against the stored current versions:
- a different hash is a local edit, so a new version is made;
- a missing file is a local delete, but only if that path was previously
  written on this machine (`materialized`). A file this machine has never
  had is not a delete.
- **Vanished-root guard:** if an entry's whole folder, or a file entry's
  parent folder, is missing, Enigma records no deletes for it. The entry is
  marked "missing locally" and an alert is raised. This covers an unmounted
  disk or a moved home folder.

### 6.6 Applying a version
- **Write:** to a temp file in the same folder, fsync, set the exec bit and
  mtime, then rename over the target. A reader never sees a half-written
  file. Missing parent folders are created (D6).
- **Precondition:** just before the rename, the target is hashed again. If
  it changed since the decision (someone is editing it right now), the write
  is abandoned and the new local edit wins the race: it becomes a local
  version and goes through §7 against the incoming one.
- **Delete:** the file is moved to the trash (§9), and folders left empty
  inside a folder entry are removed. The entry's root folder is never removed.
- **Never clobbered:** something Enigma does not sync (a symlink, a folder,
  an oversized file) sitting at a target path is not overwritten. It is
  reported as an error on that path.

## 7. Conflicts

When two versions are concurrent (neither vector is newer):

1. **Same content** (same hash): no conflict. The vectors are merged.
2. **Edit vs delete:** the edit wins (D8). The file is restored where it was
   deleted.
3. **Both text, base found:** three-way line merge (diff3) using the closest
   common ancestor as base.
   - **Ancestry:** each machine keeps, per file, the graph of recent content
     hashes and their parents (the 64 nearest the current version). `FILE`
     carries the sender's ancestry list for its version.
   - **Base:** among hashes in both ancestries, the one closest to both
     (smallest of the two distances, then smallest sum, then lowest hash).
     Both machines have the same two lists, so both pick the same base.
   - The base content comes from the local history store (§10). If it is
     not there, go to step 4.
   - Clean merge: the result becomes a new version with both versions as
     parents and the vectors merged (no extra counter, so both machines make
     the identical version). Its mtime is the later of the two; its exec bit
     and author come from the version that would win in step 4.
   - Overlapping or touching hunks: go to step 4.
4. **Fallback (binary file, no base, or overlapping edits):**
   - The newer version by mtime wins in place; on equal mtimes, the higher
     machine ID wins, then the higher content hash (a total order, so two
     machines never each keep their own).
   - The losing content is written as a sibling:
     `<stem>.conflict-<machine>-<YYYYMMDD-HHMMSS>-<hash6><ext>`, e.g.
     `CLAUDE.conflict-devbox-20261020-153012-1a2b3c.md`.
     - The time is in UTC and `<hash6>` is the start of the loser's content
       hash. Every machine derives the same name, and two different losers
       never share one, even within the same second.
     - A dotfile keeps its full name as the stem (`.zshrc.conflict-…`).
     - If the name is ever taken by other content, a numbered name is used.
       A copy is never dropped.
   - Inside a folder entry, the copy syncs to every machine like any file.
     For a single-file entry, the copy is outside every entry, so it exists
     only on the machines that resolved the conflict.
   - A desktop notification is raised on the machine that wrote the copy.

**Text detection:** valid UTF-8 and no NUL bytes in the first 8 KB.

**Determinism.** Both machines usually detect the same conflict at the same
time. Every decision above uses only data both sides have (the two versions,
their mtimes, machine IDs and the base), so both produce the same merge
result or the same conflict copy name and content. Identical results
collapse by rule 1, so a conflict is resolved once, not twice.

**Clock skew** can make the wrong edit "newer" in step 4. Nothing is lost
either way: the other edit is in the conflict copy.

## 8. What can be synced

- **Entries:** paths under `~`, stored relative to `~`. Absolute paths
  outside `~` are refused (D16).
- **Regular files only.** Symlinks, sockets and devices are skipped and
  listed in status (D15). Only the exec bit of the permissions is synced.
- **Always ignored:** `.git/`, `.hg/`, `.svn/`, `node_modules/`, `.DS_Store`,
  `*.swp`, `*.swx`, `*~`, `.#*`, `#*#`, Enigma temp files.
- **Per-entry ignores:** extra globs. A pattern without `/` matches any path
  component; a pattern with `/` matches from the entry's root.
- **Limits** (group settings): 10 MB per file and 2,000 files per folder
  entry. A file over the limit is not synced and shows in status. A folder
  over the limit refuses to be added.
- **Overlap:** an entry inside another entry is refused. Adding a folder
  that contains existing entries offers to replace them.

## 9. Adding, removing and the trash

**Adding an entry** (D7), with `enigma add ~/.claude/skills` on machine X:
1. X records the entry in group state and makes version `{X:1}` of every
   file under it.
2. Every other machine, on receiving the entry, compares its own files at
   that path. If a file exists with different content, the machine backs
   up its copy locally (D13), then writes X's version.
3. Files that exist only on the other machine, inside the folder, are kept
   and become new versions there, so they spread to every machine (D14).
   The folder ends up as X's files plus everyone else's extras.

**Removing an entry** (`enigma remove <path>`): it stops syncing on all
machines. The files stay as they are everywhere.

**Trash:** `~/.local/share/enigma/trash/<YYYY-MM-DD>/<path>`, purged after
30 days (a group setting). Trash is per machine and never synced. `enigma
trash list` and `enigma trash restore <id>` restore a file, which then syncs
as a normal edit. Restore never overwrites an existing file.
- The same path trashed twice in a day goes to `<YYYY-MM-DD>.2/<path>`
  (then `.3`, …), so the original path is always everything after the
  first folder. The ID is `<day folder>/<path>`.

## 10. Local storage (per machine)

| Path | Content |
|---|---|
| `~/.config/enigma/config.toml` | Local-only settings: alias overrides, notifications on/off. Group settings are not here. |
| `~/.local/share/enigma/state/` | Group state and current versions, as JSON files written atomically (temp + rename). |
| `~/.local/share/enigma/history/` | Content of past versions, by hash, used as merge bases. A blob is kept while it is among the last 20 versions of some file or younger than 30 days; the store is capped at 200 MB, oldest first, never dropping a current version. |
| `~/.local/share/enigma/trash/` | §9 |
| `~/.local/share/enigma/backups/` | First-add backups (§9, D13) |
| `~/.local/share/enigma/enigma.sock` | API socket, used by the CLI and TUI (HTTP + JSON). |
| `~/.local/share/enigma/peer.sock` | Peer socket: `enigma bridge` relays an incoming ssh link here. |
| `~/.local/share/enigma/identity.json` | This machine's ID, group ID and name. |
| `~/.local/share/enigma/bin/` | `enigma-<os>-<arch>` binaries for adding and upgrading other platforms (`make dist`). |

Sockets: macOS limits socket paths to 104 bytes, so a longer path falls
back to `/tmp/enigma-<uid>-<hash>-<name>.sock`. The CLI and the bridge find
the sockets the same way.
| `~/.local/share/enigma/enigma.log` | Daemon log, rotated at 10 MB. |

`XDG_CONFIG_HOME` and `XDG_DATA_HOME` are respected. `ENIGMA_HOME` puts
everything under one folder, for tests.

**Tombstones** (deleted-file versions and removed records) are kept 90 days.
A machine offline for longer is told to rejoin, which runs a first add from
an online machine. Otherwise it could bring deleted files back.

## 11. Machines

**Machine records** are part of the replicated group state (§6.4), one per
member, last writer wins like entries:
`{machine_id, name, alias, target, bin, clock, by, removed}`.
- `alias`: the ssh alias it was added under; `target`:
  `ssh://user@hostname:port` captured with `ssh -G` on the adding machine;
  `bin`: path of its enigma binary.
- Every machine dials every other member, resolving the target as in §5.
- The first machine (from `init`) was never added, so it has no target: it
  dials the others, and one direction per pair is enough (§5).
  `enigma machine alias` overrides this locally.

**Creating the group:** `enigma init` on the first machine creates the group
(random `group_id`) with that machine as its only member.

**Adding a machine:** `enigma machine add <ssh-alias>`, from any member.
Every step is printed with the exact command it runs (D9):
1. `ssh <alias> uname -sm` to detect the OS and CPU; unsupported platforms
   stop here.
2. Get a binary for that platform: this binary itself if it matches,
   otherwise `<data>/bin/enigma-<os>-<arch>` (filled by `make dist` before
   releases exist), otherwise the matching release asset of this version
   from `nadavushka-dev/enigma`, checksum-verified (D17). If none is found,
   stop and say what to run.
3. Copy it to `~/.local/bin/enigma` on the remote, with a temp name and
   rename.
4. Run `enigma join` on the remote, passing the group (ID, name, member
   records) over stdin. It creates the remote's identity and prints its
   machine ID.
5. Record the machine (§11 records) in group state through the local
   daemon. Every machine then links to it, and it receives all entries
   (first-add rules apply).
6. Run `enigma service install` on the remote, which starts the daemon.
   - Linux: the systemd user unit, plus `loginctl enable-linger` so it keeps
     running after logout. Without a systemd user session (containers, WSL,
     minimal servers) the daemon is started in the background instead, with
     a warning that it will not survive a reboot.
7. **Connectivity check:** the new machine reports which peers it could
   link to. Missing pairs are shown as warnings (D1 is assumed, not
   guaranteed).

**Removing a machine:** `enigma machine remove <name> [--uninstall]`. Its
record is marked removed, the others stop linking to it, and the handshake
refuses it. If it is reachable, it is told to leave (`enigma leave` over
ssh): its daemon stops, it forgets the group and keeps its files, and with
`--uninstall` the service and binary are removed too.

**Installing and updating** (releases, D17):
- GoReleaser builds `enigma_<version>_<os>_<arch>.tar.gz` for macOS and
  Linux (amd64, arm64) plus `checksums.txt` (sha256) on every `v*` tag.
- `curl -fsSL https://raw.githubusercontent.com/nadavushka-dev/enigma/main/install.sh | sh`
  picks the asset for the machine, verifies its checksum and installs to
  `~/.local/bin` (`/usr/local/bin` with sudo as a fallback), with a PATH
  hint. `ENIGMA_VERSION` pins a release. Next step: `enigma init` on the
  first machine, `enigma machine add` for the others.
- `enigma update` moves this binary to the latest release in place (same
  checksum check, atomic swap) and restarts the daemon; the group then
  follows over the links (below). `--check` only reports.

**Names:** a machine's display name is the alias it was added under; it can
be renamed (`enigma machine rename`). Inside Enigma a machine is identified
by a random `machine_id` created at install.

**One version everywhere.** Linked machines always run the same Enigma
version.
- `HELLO` carries the Enigma version and the platform (`os/arch`). A link
  between different versions syncs nothing: it only carries an upgrade.
- The newer side sends its binary for the older side's platform over the
  link (this binary, or the `<data>/bin` cache). It works whichever side
  dialed, so one-way ssh is enough.
- The older side checks the hash and that the version is really newer
  (never a downgrade), replaces its own binary atomically, and re-executes
  itself. The link then comes back with matching versions and catches up.
- If no binary for that platform is at hand, both sides show it in status
  (`cannot upgrade <machine>: no linux/arm64 binary; run make dist`).
- Upgrading one machine (a new binary and a daemon restart) therefore
  upgrades the whole group. Version order: releases by semver; dev builds
  (`make build`) carry the commit time, so a newer build wins.

## 12. Interface

### 12.1 CLI
Every command takes `--json` for agents and scripts.

| Command | Does |
|---|---|
| `enigma init` | create the group on this machine |
| `enigma machine add <alias>` · `list` · `remove <name> [--uninstall]` · `rename <name> <new>` · `alias <name> <ssh-alias>` | manage machines (upgrades are automatic, §11) |
| `enigma add <path> [--ignore GLOB]…` · `remove <path>` · `list` | manage entries |
| `enigma status` | machines (link up/down, latency, last seen), entries, pending transfers, errors |
| `enigma conflicts [--local]` | list conflict copies across the group (asks every member over ssh), with the machines that hold each |
| `enigma trash list` · `restore <id>` · `empty` | trash on this machine |
| `enigma log [-f]` | daemon log |
| `enigma doctor` | connectivity matrix between all machines (each asked over ssh), versions, daemon and service check; exits non-zero on problems |
| `enigma tui` | the TUI; also what bare `enigma` runs in a terminal |
| `enigma daemon` · `service install` / `uninstall` | run in the foreground / as a background service |
| `enigma bridge` · `join` · `leave` | internal, used over ssh |
| `enigma version` · `update [--check]` | version / update from the latest release |

### 12.2 TUI
Three panes:
- **Machines:** hosts from `~/.ssh/config` (and its `Include`s, without
  wildcard patterns). Members show link status; pressing `a` on a non-member
  adds it, through the same flow as the CLI, showing each step.
- **Files:** a file browser rooted at `~`. Entries are marked; `a` adds the
  selected file or folder, `x` removes it. Ignored and oversized items are
  dimmed.
- **Activity:** live sync events, conflicts and errors. `enter` on a
  conflict shows the two versions side by side.

Keys are vim-style, with `?` for help. Library: bubbletea, bubbles and
lipgloss.

### 12.3 Notifications
For a conflict copy, a vanished entry root, or a failed merge write:
- macOS: `osascript -e 'display notification …'`;
- Linux: `notify-send`, if present.

Rate-limited to one notification per path per 10 minutes. Can be turned off
in `config.toml`.

## 13. Engineering

- **Language:** Go (current stable).
- **Dependencies:** stdlib, `fsnotify`, `BurntSushi/toml`, and
  `bubbletea`/`bubbles`/`lipgloss` for the TUI. Anything else needs approval.
- **Package layout:**
  - `cmd/enigma`: commands only
  - `internal/fsx`: hashing, atomic write, ignore rules
  - `internal/watch`: watcher
  - `internal/version`: vectors and ancestry
  - `internal/merge`: diff3
  - `internal/engine`: decisions; pure where possible
  - `internal/group`: replicated group state
  - `internal/proto`: frames and messages
  - `internal/link`: ssh links and dialing
  - `internal/daemon`: the daemon process
  - `internal/store`: state, history, trash
  - `internal/install`: machine setup over ssh
  - `internal/service`: launchd and systemd
  - `internal/notify`
  - `internal/tui`
- **Wire format:** length-prefixed frames, each a JSON header followed by
  raw content bytes. `proto_version` is bumped on any incompatible change.
- **Testing:**
  - Unit tests: vectors, diff3, ignore rules, conflict naming.
  - Simulation tests: N daemons in one process, links over in-memory pipes,
    random edits, deletes, disconnects and restarts. The invariant: once
    quiet, every machine has identical content and no data is lost. Every
    overwritten version must exist somewhere as current content, a
    conflict copy, the trash or a backup.
  - End-to-end (from milestone 2), with no second physical machine needed:
    - 3–4 Linux Docker containers, each running `sshd` and an Enigma
      daemon, plus the development Mac as a full member. The Mac covers
      the macOS-only parts (watcher, launchd, notifications).
    - Scripted scenarios: `machine add` into a fresh container; cutting a
      machine off with `docker network disconnect` / `docker pause` to test
      offline catch-up and changes relayed around a dead link; killing and
      restarting daemons; concurrent edits for conflicts.
    - systemd runs in a systemd-enabled test image.
    - Clock skew is covered by unit tests, since containers share the host
      clock.
  - Before a release: one manual check on real machines (sleep/wake, real
    networks).
  - Always run with `go test -race`.
- **Releases:** goreleaser builds darwin/linux × amd64/arm64 with
  `checksums.txt`.
- **Dev workflow (D12):**
  - tasks in `./todo.txt` with tuxedo;
  - review with `tuicr` before commits;
  - parallel agent work in herdr workspaces.

### Development method: TDD (regression is the enemy)
- **Red, green, refactor.** Every behavior starts as a failing test, then
  gets the smallest code that passes, then a cleanup with the suite green.
  No production code is written without a failing test asking for it.
- **Bugs start as tests.** A bug fix begins with a test that reproduces it
  and fails. The test stays in the suite forever.
- **Existing tests are a contract.** They are never deleted, skipped or
  weakened to make a change pass. If a decision really changes expected
  behavior, the test change is called out and agreed first.
- **Spec decisions are tests.** Every decision D1–D18 and every rule in
  §6–§9 has at least one test named after it (e.g. `TestD8_EditBeatsDelete`),
  so a regression points at the decision it breaks.
- **Gates:** `go test -race ./...` must pass before every commit, enforced
  by a git pre-commit hook along with `gofmt` and `go vet`. CI runs the same
  checks plus the simulation suite with more random seeds. A failing seed is
  turned into a fixed regression test.
- **Docker end-to-end tests** run before each milestone is closed and
  before every release.

### Milestones
1. **Core engine:** fsx, watch, version, engine. Two machines in one process
   over pipes, with no merge yet.
2. **Daemon and ssh links:** service install, the ssh bridge, catch-up,
   basic CLI (`init`, `add`, `status`). Machines are installed by hand.
3. **Conflicts:** diff3 merge, conflict copies, trash, first-add backups,
   history store.
4. **Machines:** replicated machine records, `machine add` with self-install,
   automatic same-version upgrades over the link, `doctor`, releases with
   `install.sh` and `enigma update`.
5. **TUI.**
6. **Polish:** notifications, signed/notarized binaries, end-to-end tests.

Each milestone ends with all tests passing and a review in tuicr.

## 14. Resolved questions

| Question | Resolution |
|---|---|
| Where do first-add backups go? | Local backup folder (D13). |
| First add of a folder: files only on another machine | Kept and spread (D14). |
| Symlinks | Skipped (D15). |
| Paths outside `~` | Not supported (D16). |
| Binary distribution | `github.com/nadavushka-dev/enigma` (D17). |
| Limits | 10 MB / 2,000 files (D18). |

# Enigma — design spec

Status: **agreed**, 2026-10-20; groups, problems and the fleet rename added 2026-10-26 (D19–D21).
Milestones 1–6 are implemented (v0.2.0); milestone 7 is the 2026-10-26 additions.
Sections 1–3 record what was decided in discussion. Sections 4 onward are
the technical design. Section 15 records how the remaining open points were
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
| D5 | Machines | One fleet containing all your machines, picked from your ssh config. No per-file machine lists and no exclusions (superseded by D19: groups). |
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
| D18 | Limits | 10 MB per file, 2,000 files per folder entry, by default; a fleet setting (`enigma limits`). |
| D19 | Groups | A group is a named set of entries with a member list. Every entry is in exactly one group; a machine syncs an entry only if it is a member of its group, and never sees the paths of groups it is not in. `enigma init` creates `main`; new machines and new entries go to `main` unless told otherwise. A path can be in one group only; entries move between groups, and groups merge. |
| D20 | Problems | The daemon keeps a list of standing problems (what is wrong right now, with a hint), shown by `enigma status`, `doctor` and a notice line in the TUI, until each is resolved. |
| D21 | TUI parity | Everything the CLI can do, the TUI can do. |

## 3. Vocabulary

- **Machine:** a computer in the fleet, running the Enigma daemon.
- **Fleet:** all your machines: the ones that know each other, link to each
  other and upgrade each other. There is exactly one. Called "the group" in
  code older than D19.
- **Group:** a named set of entries plus the machines that sync them (§9).
  `main` always exists.
- **Entry:** a file or folder you chose to sync, stored as a path relative to
  `~` (e.g. `.claude/CLAUDE.md`, `.claude/skills/`). It belongs to one group.
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
 │  ├ fleet state              │   bridge"   │ enigma bridge (stdio ⇄ sock)│
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
  Pings and the silence check belong to the link, not to the engine loop:
  a machine busy on a long operation keeps its links, and time a frame
  spends waiting for the busy loop does not count as silence.
- **Reconnect** with backoff of 1 s doubling up to 60 s. Every reconnect
  starts with a catch-up (§6.5).
- **Handshake:** `HELLO {fleet_id, machine_id, proto_version, enigma_version, platform}`.
  A different fleet or a removed machine closes the link with a clear
  error, shown in status. A different Enigma version turns the link into
  an upgrade (§12).

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
- A file is read only when it may have changed. With each hash Enigma keeps
  the file's lstat stamp (size, mtime, ctime, inode, mode); while the stamp
  is unchanged, the hash is reused. An in-place edit moves the mtime and the
  ctime (which no tool can set back), a replaced file has a new inode. A
  stamp younger than 2 s is not kept, since on coarse timestamps an edit
  right after the hash could leave it unchanged. Files over the size limit
  are never read. So a rescan of unchanged entries reads nothing.
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

### 6.4 Fleet state
The machine list, the groups, the entries and fleet settings form the
**fleet state**, replicated to every machine. Machine, group and settings
records go to every member; an entry record goes only to the members of its
group (§9).
- Each record (a machine, a group, an entry) carries a Lamport timestamp and the ID of
  the machine that last changed it. The highest timestamp wins, with the
  machine ID as tie-break. Removed records stay behind as markers ("tombstones").
- On connect and on every change, machines exchange records the same way they
  exchange file versions.
- This is how adding an entry on any machine reaches all machines (D5, D6).

### 6.5 Catch-up after being offline
When a link comes up, the two machines exchange a summary
`{path: (hash, vector)}` of all tracked files. Each side then treats every
difference as an announcement (§6.3). Fleet state is exchanged first, so new
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
- **Delete:** the file is moved to the trash (§10), and folders left empty
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
   - The base content comes from the local history store (§11). If it is
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
- **Limits** (fleet settings, §6.4): by default 10 MB per file and 2,000
  files per folder entry. `enigma limits` shows them and changes them for
  the whole fleet; the latest change wins, and every machine rescans its
  entries. Ceilings: 64 MB per file, since a file travels as one message
  (§14), and 20,000 files per folder.
  - A file over the size limit is not synced and shows in status as
    skipped. A machine also refuses a received file over its limit, with an
    error in its activity.
  - A folder over the file limit refuses to be added, without reading it.
  - A folder that grows past the limit later keeps syncing the files it
    has. New files past the limit are not synced, and status, the TUI, the
    activity feed and a notification say so once (`~/.claude: over the
    2,000-file limit, 431 new files not synced`). They sync as soon as there
    is room again: files removed, or the limit raised.
- **Overlap:** an entry inside another entry is refused, whatever the
  group: a path is synced by one entry in one group (§9). Adding a folder
  that contains existing entries offers to replace them.

## 9. Groups

A **group** is a named set of entries and the machines that sync them (D19).
Membership is the only thing that decides who syncs what; the version
machinery (§6, §7) is unchanged.

- **`main`** is created by `enigma init` with the first machine in it. New
  machines join `main` unless `machine add --group` says otherwise, and
  `enigma add` puts an entry in `main` unless `--group` says otherwise.
  `main` can be emptied but not deleted or renamed.
- **Records.** A `GroupRecord {name, members [machine_id], clock, by,
  removed}` in fleet state, last writer wins (§6.4), replicated to every
  member of the fleet, so groups are managed from any machine. An
  `EntryRecord` carries its `group`.
- **Scope.** Machine X syncs entry E when X is a member of E's group. E's
  record, announcements, fetches and summaries (§6.5) travel only over links
  whose both ends are in E's group. A non-member never learns E's paths:
  not in `enigma list`, status or the TUI. The price is that E cannot relay
  through a non-member while a link between members is down; it catches up
  when the link returns.
- **One path, one group.** A path is synced by one entry in one group.
  Adding a path that another group already syncs is refused with
  `~/.zshrc is already synced in main; use enigma entry move`.
- **Adding an entry** (`enigma add --group G`) is allowed only from a member
  of G.
- **Entering scope.** When a machine becomes a member of a group (`group
  add`, `machine add --group`, an entry moved into its group) it runs the
  first-add rules (§10, D7, D13, D14) for every entry it did not sync
  before: a differing local copy is backed up, extras in a folder are kept
  and spread.
- **Leaving scope** (`group remove`, an entry moved to a group it is not in)
  is `enigma remove` for that machine alone: syncing stops, files stay, the
  machine keeps its stored versions for the paths. If it re-enters scope,
  an edit made meanwhile is a concurrent version and goes through §7.
- **Moving an entry** (`enigma entry move <path> <group>`): members of both
  groups continue; members only of the new group enter scope; members only
  of the old group leave scope. Only a member of both groups may move an entry.
- **Merging** (`enigma group merge pair main`) moves every entry of `pair`
  into `main`, then deletes `pair`. It first prints the plan (which machines
  gain or lose which entries) and asks. A member of `pair` that is not in
  `main` would lose entries, so the merge is refused unless
  `--add-members` adds it to `main` first.
- **Deleting a group** is refused while it has entries. Removed group
  records are tombstones (§11).
- **Upgrade from v0.2.** The first daemon running this version creates
  `main` with every member and puts every existing entry in it, so a running
  fleet behaves exactly as before.

## 10. Adding, removing and the trash

**Adding an entry** (D7), with `enigma add ~/.claude/skills` on machine X
(into `main`, or the group given with `--group`, §9):
1. X records the entry in fleet state and makes version `{X:1}` of every
   file under it.
2. Every other member of the group, on receiving the entry, compares its own files at
   that path. If a file exists with different content, the machine backs
   up its copy locally (D13), then writes X's version.
3. Files that exist only on the other machine, inside the folder, are kept
   and become new versions there, so they spread to every machine (D14).
   The folder ends up as X's files plus everyone else's extras.

**Removing an entry** (`enigma remove <path>`): it stops syncing on all
machines. The files stay as they are everywhere.

**Trash:** `~/.local/share/enigma/trash/<YYYY-MM-DD>/<path>`, purged after
30 days (a fleet setting). Trash is per machine and never synced. `enigma
trash list` and `enigma trash restore <id>` restore a file, which then syncs
as a normal edit. Restore never overwrites an existing file.
- The same path trashed twice in a day goes to `<YYYY-MM-DD>.2/<path>`
  (then `.3`, …), so the original path is always everything after the
  first folder. The ID is `<day folder>/<path>`.

## 11. Local storage (per machine)

| Path | Content |
|---|---|
| `~/.config/enigma/config.toml` | Local-only settings: alias overrides (`[aliases]`), `notifications = false` to turn desktop notifications off (§13.3). Fleet settings are not here. |
| `~/.local/share/enigma/state/` | Fleet state and current versions, as JSON files written atomically (temp + rename). |
| `~/.local/share/enigma/history/` | Content of past versions, by hash, used as merge bases. A blob is kept while it is among the last 20 versions of some file or younger than 30 days; the store is capped at 200 MB, oldest first, never dropping a current version. |
| `~/.local/share/enigma/trash/` | §10 |
| `~/.local/share/enigma/backups/` | First-add backups (§10, D13) |
| `~/.local/share/enigma/enigma.sock` | API socket, used by the CLI and TUI (HTTP + JSON). |
| `~/.local/share/enigma/peer.sock` | Peer socket: `enigma bridge` relays an incoming ssh link here. |
| `~/.local/share/enigma/identity.json` | This machine's ID, fleet ID and name. |
| `~/.local/share/enigma/bin/<version>/` | `enigma-<os>-<arch>` binaries for adding and upgrading other platforms (`make dist`, release downloads). |

Sockets: macOS limits socket paths to 104 bytes, so a longer path falls
back to `/tmp/enigma-<uid>-<hash>-<name>.sock`. The CLI and the bridge find
the sockets the same way.
| `~/.local/share/enigma/enigma.log` | Daemon log, rotated at 10 MB. |

`XDG_CONFIG_HOME` and `XDG_DATA_HOME` are respected. `ENIGMA_HOME` puts
everything under one folder, for tests.

**Tombstones** (deleted-file versions and removed records) are kept 90 days.
A machine offline for longer is told to rejoin, which runs a first add from
an online machine. Otherwise it could bring deleted files back.

## 12. Machines

**Machine records** are part of the replicated fleet state (§6.4), one per
member, last writer wins like entries:
`{machine_id, name, alias, target, bin, clock, by, removed}`.
- `alias`: the ssh alias it was added under; `target`:
  `ssh://user@hostname:port` captured with `ssh -G` on the adding machine;
  `bin`: path of its enigma binary.
- Every machine dials every other member, resolving the target as in §5.
- The first machine (from `init`) was never added, so it has no target: it
  dials the others, and one direction per pair is enough (§5).
  `enigma machine alias` overrides this locally.

**Creating the fleet:** `enigma init` on the first machine creates the fleet
(random `fleet_id`) with that machine as its only member, and the group
`main` with that machine in it.

**Adding a machine:** `enigma machine add <ssh-alias>`, from any member.
Every step is printed with the exact command it runs (D9):
1. `ssh <alias> uname -sm` to detect the OS and CPU; unsupported platforms
   stop here.
2. Get a binary for that platform: this binary itself if it matches,
   otherwise `<data>/bin/<version>/enigma-<os>-<arch>` (filled by `make dist`
   and by downloads), otherwise the matching release asset of this version
   from `nadavushka-dev/enigma`, checksum-verified (D17). If none is found,
   stop and say what to run.
3. Copy it to `~/.local/bin/enigma` on the remote, with a temp name and
   rename.
4. Run `enigma join` on the remote, passing the fleet (ID, name, member
   records) over stdin. It creates the remote's identity and prints its
   machine ID.
5. Record the machine (§12 records) in fleet state through the local
   daemon, as a member of `main` (or of the groups given with `--group`,
   repeatable; `--group none` for no group yet). Every machine then links
   to it, and it receives the entries of its groups (first-add rules apply).
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
ssh): its daemon stops, it forgets the fleet and keeps its files, and with
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
  checksum check, atomic swap) and restarts the daemon; the fleet then
  follows over the links (below). `--check` only reports.

**Names:** a machine's display name is the alias it was added under; it can
be renamed (`enigma machine rename`). Inside Enigma a machine is identified
by a random `machine_id` created at install.

**One version everywhere.** Linked machines always run the same Enigma
version.
- `HELLO` carries the Enigma version and the platform (`os/arch`). A link
  between different versions syncs nothing: it only carries an upgrade.
- The newer side sends its binary for the older side's platform over the
  link (this binary, the `<data>/bin/<version>` cache, or its release
  download). It works whichever side
  dialed, so one-way ssh is enough.
- The older side checks the hash and that the version is really newer
  (never a downgrade), replaces its own binary atomically, and re-executes
  itself. The link then comes back with matching versions and catches up.
- If no binary for that platform is at hand, both sides show it in status
  (`cannot upgrade <machine>: no linux/arm64 binary; run make dist`).
- Upgrading one machine (a new binary and a daemon restart) therefore
  upgrades the whole fleet. Version order: releases by semver; dev builds
  (`make build`) carry the commit time, so a newer build wins.
- **Trust.** A member can hand any linked machine a binary that machine then
  runs, whichever side dialed. Joining a fleet therefore means trusting
  every member with code on this machine, as with ssh access to it. Only
  add machines you control.

## 13. Interface

### 13.1 CLI
Every command takes `--json` for agents and scripts.

| Command | Does |
|---|---|
| `enigma init` | create the fleet on this machine, with the group `main` |
| `enigma machine add <alias> [--group G]…` · `list` · `remove <name> [--uninstall]` · `rename <name> <new>` · `alias <name> <ssh-alias>` | manage machines (upgrades are automatic, §12) |
| `enigma group list` · `create <name> [machine…]` · `delete <name>` · `rename <name> <new>` · `add <name> <machine>…` · `remove <name> <machine>…` · `merge <from> <into> [--add-members]` | manage groups (§9) |
| `enigma entry move <path> <group>` | move an entry to another group (§9) |
| `enigma add <path> [--group G] [--ignore GLOB]…` · `remove <path>` · `list` | manage entries (`--group` defaults to `main`); `add` with no path picks files and folders with `fzf` (§13.4) |
| `enigma limits [--file-size SIZE] [--files N]` | show the fleet's limits, or change them for every machine (§8); SIZE like `25MB` |
| `enigma status` | problems (§13.5), machines (link up/down, latency, last seen), groups, entries, pending transfers |
| `enigma conflicts [--local]` | list conflict copies across the fleet (asks every member over ssh), with the machines that hold each |
| `enigma trash list` · `restore <id>` · `empty` | trash on this machine |
| `enigma log [-f]` | daemon log |
| `enigma doctor` | the standing problems (§13.5), connectivity matrix between all machines (each asked over ssh), versions, daemon and service check; exits non-zero on problems |
| `enigma tui` | the TUI; also what bare `enigma` runs in a terminal |
| `enigma daemon` · `service install` / `uninstall` | run in the foreground / as a background service |
| `enigma bridge` · `join` · `leave` | internal, used over ssh |
| `enigma version` · `update [--check]` | version / update from the latest release |

### 13.2 TUI
`enigma tui`, and bare `enigma` in a terminal. A client of the local daemon
(it needs one running). Everything the CLI does is reachable here (D21).
Layout: three panes in a row (Machines, Groups, Files), an activity strip,
a notice line (§13.5), and the key hints.

```
┌ Machines ────────┐┌ Groups ─────────────┐┌ Files ~/ ──────────────────┐
│ ● mac    this    ││ main  mac devbox    ││   .claude/      main       │
│ ● devbox linked  ││       laptop  12    ││ ▸   CLAUDE.md   main       │
│ ○ laptop down    ││ pair  mac devbox  3 ││   notes/        pair       │
│ · nas    ssh only││                     ││   Downloads/               │
└──────────────────┘└─────────────────────┘└────────────────────────────┘
┌ Activity ───────────────────────────────────────────────────────────┐
│ 14:02 CLAUDE.md ← devbox                                            │
│ 14:05 ! conflict notes/todo.md (copy kept)                          │
└─────────────────────────────────────────────────────────────────────┘
 ! 2 problems: laptop link down since 14:02 (ssh: connection refused)   p
 tab focus  a add  x remove  enter open  p problems  L limits  ? help  q quit
```

- **Machines:** members with their link state, then hosts from
  `~/.ssh/config` (and its `Include`s, without wildcard patterns) that are
  not members. `a` on a host adds it (the `machine add` steps, shown live);
  `x` on a member removes it, after a confirmation (`u` also uninstalls).
  Adding asks which groups the machine joins (default `main`). `enter` on a
  member lists its groups; `a`/`x` there add it to or remove it from one.
- **Groups:** every group with its members and entry count. `n` creates one
  (prompts for the name and members), `r` renames, `x` deletes (refused with
  a notice while it has entries), `enter` opens the member list where
  `a`/`x` add and remove machines, `M` merges it into another group after
  showing the plan (§9). Membership changes ask for confirmation, since they
  start first adds or stop syncing.
- **Files:** a lazy tree rooted at `~`, dotfiles included. Synced entries
  are marked, and so is a folder that contains one (`contains synced`);
  ignored, oversized and non-regular items are dimmed, and files kept out
  by the folder limit are marked `over limit` (§8). Each synced row shows
  its group. `a` adds the selected file or folder, asking for the group when
  there is more than one (default `main`); `m` moves an entry to another
  group; `x` stops syncing an entry (after a confirmation). `/` filters it
  with fuzzy search (§13.4).
- **Activity:** the daemon's recent events (a ring of the last 500: changes
  received and made, merges, conflict copies, deletes, links up and down,
  upgrades, errors), live. `enter` on a conflict shows the original and the
  copy side by side, differing lines marked.
- **Prompts:** names, machine lists, the group for an add and the limits
  (`L`, §8) are typed into a one-line prompt (bubbles `textinput`), with
  `esc` to cancel. Lists of machines or groups are picked with `space`.
- Keys: `tab`/`shift+tab` move focus; `j`/`k`, `g`/`G`, `ctrl+d`/`ctrl+u`;
  `l`/`enter` opens a folder, `h` closes it; `/` search; `p` problems;
  `L` limits; `?` help; `q` quits.
- Library: bubbletea, bubbles and lipgloss.

### 13.4 Fuzzy search
Finding a file to add without walking the tree by hand.

- **Matcher** (`internal/fuzzy`, stdlib only): the query's characters must
  appear in the path in order. Case is ignored unless the query has an upper
  case letter. Spaces split the query into terms that must all match. The
  score rewards consecutive characters, characters at the start of a
  segment (after `/`, `.`, `_`, `-`) and matches in the base name; ties go to
  the shorter path. Matched characters are highlighted.
- **Home index:** a background walk of `~` that skips the always-ignored
  names (§8), `~/Library`, `.cache`, `.Trash` and Enigma's own data folder,
  goes at most 8 levels deep and stops at 50,000 paths (and says so). It
  only lists names: no file is read. Built on first use, off the engine
  (the TUI and the CLI build it themselves; the daemon is not involved).
- **TUI:** `/` in the Files pane opens a filter line; the rows become a flat
  list of matching paths, best first. Two scopes, switched with `tab`:
  **tree** (the folders opened so far, instant) and **~** (the home index;
  `indexing… N` while it builds). `↑`/`↓` or `ctrl+p`/`ctrl+n` move;
  `enter` closes the filter and puts the cursor on that path in the tree,
  opening its parents, so `a` and `x` work as usual; `esc` cancels and
  restores the cursor.
- **CLI:** `enigma add` with no path, in a terminal, with `fzf` on `PATH`,
  pipes the home index into `fzf --multi` and adds each pick. Without
  `fzf` (or without a terminal) it fails with `give a path, or install fzf
  to pick one`. `fzf` is optional and used only here; nothing else depends
  on it.

### 13.3 Notifications
For a conflict copy, a vanished entry root, or a failed merge write:
- macOS: `osascript -e 'display notification …'`; the text goes in as
  AppleScript string literals and the script is passed as an argument,
  never through a shell;
- Linux: `notify-send`, if present on `PATH`; otherwise nothing.

Rate-limited to one notification per path per 10 minutes. Turned off with
`notifications = false` in `config.toml` (read when the daemon starts).
Notifying never blocks the engine loop: the command runs on its own
goroutine and nothing waits for it. The notifier is injected into the
daemon, so tests never raise real notifications.

### 13.5 Problems
The activity feed says what happened; **problems** say what is wrong now
(D20). The daemon keeps a set of standing problems, each
`{key, severity (error|warning), since, message, hint, path?, peer?}`,
derived from state, not from the log: a problem appears when its condition
starts and disappears when it ends, so the list is always true.

Conditions, with their hints:
- a member's link is down (the ssh error, since when; hint: `enigma doctor`,
  or `enigma machine alias` for a founder with no target);
- a member on another version that cannot be upgraded (no binary for its
  platform; hint: `make dist` / `enigma update`);
- an entry missing locally (vanished root, §6.5); an entry over the file
  limit (§8); files skipped as oversized or non-regular (one problem per
  entry, counting them);
- a path that cannot be written: something unsyncable sits there (§6.6) or
  a permission error;
- a conflict copy written in the last hour (warning; it clears by itself);
- a group with no members; an entry of a group this machine is not in
  (cannot happen by construction, reported if it does);
- daemon level: watcher error, failed rescan, history store at its cap,
  a notification command failing.

Exposed as `GET /problems` on the API socket. `enigma status` prints the
list first; `enigma doctor` includes it and exits non-zero when any
error-level problem stands. Desktop notifications (§13.3) stay as they are.

**TUI notice line:** one line between the activity strip and the key hints.
With problems: `! N problems: <newest or most severe>`, red for errors,
yellow for warnings. With none: `all good`, dim. The result of an action
(`added ~/.claude to pair`, `laptop added to main`) shows there for a few
seconds; a failed action (`refused: ~/.zshrc is already synced in main`)
stays in red until the next key. `p` opens the full list as a modal, oldest
first, each with its hint; `enter` on one jumps to the related row in the
Machines, Groups or Files pane when there is one.

## 14. Engineering

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
  - `internal/fleet`: replicated fleet state (was `internal/group`)
  - `internal/proto`: frames and messages
  - `internal/link`: ssh links and dialing
  - `internal/daemon`: the daemon process
  - `internal/store`: state, history, trash
  - `internal/install`: machine setup over ssh
  - `internal/service`: launchd and systemd
  - `internal/notify`
  - `internal/problems`: standing problems (§13.5)
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
  §6–§10 has at least one test named after it (e.g. `TestD8_EditBeatsDelete`),
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
6. **Polish:** notifications, fuzzy search (§13.4), end-to-end tests.
   No code signing beyond Go's automatic ad-hoc signature: binaries arrive
   through curl and self-update, which macOS does not quarantine.
7. **Groups and problems** (v0.3.0, `proto_version` bump): standing problems
   and the TUI notice line (§13.5); the TUI prompt widget and limits editing;
   the fleet rename; groups in fleet state, scoped replication, CLI (§9);
   groups in the TUI (§13.2). Order: problems first, since they help debug
   the rest.

Each milestone ends with all tests passing and a review in tuicr.

## 15. Resolved questions

| Question | Resolution |
|---|---|
| Where do first-add backups go? | Local backup folder (D13). |
| First add of a folder: files only on another machine | Kept and spread (D14). |
| Symlinks | Skipped (D15). |
| Paths outside `~` | Not supported (D16). |
| Binary distribution | `github.com/nadavushka-dev/enigma` (D17). |
| Limits | 10 MB / 2,000 files by default, per fleet (D18, §8). |
| Sharing some files with some machines | Groups (D19, §9). The fleet keeps the old meaning of "group" in code until the rename. |

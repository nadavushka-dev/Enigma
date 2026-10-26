<div align="center">

# Enigma

**Keep your dotfiles, agent rules and notes identical on every machine. Live, over plain ssh.**

Edit `~/.claude/CLAUDE.md` on your laptop and it's already on your devbox.
No server, no account, no open ports: if you can `ssh` to it, Enigma can sync it.

[![CI](https://github.com/nadavushka-dev/enigma/actions/workflows/ci.yml/badge.svg)](https://github.com/nadavushka-dev/enigma/actions/workflows/ci.yml)
![Go](https://img.shields.io/badge/go-1.26+-00ADD8?logo=go&logoColor=white)
![Platforms](https://img.shields.io/badge/macOS%20%7C%20Linux-amd64%20%7C%20arm64-555)
![License](https://img.shields.io/badge/license-MIT-blue)

<img src="demo/sync.gif" alt="enigma init, machine add devbox, add ~/.claude, an edit on devbox arriving on the mac" width="900">

</div>

## Why

You work on more than one machine, and the small files that shape how you
work drift apart: shell config, editor settings, agent rules and skills,
scratch notes. Git is too heavy for them (commit, push, pull, on every
machine, every time) and cloud folders want everything under one folder and
an account.

Enigma is a small daemon on each machine. You pick files and folders under
`~`, and from then on they stay the same everywhere, within a second or two.

- **Real time:** a save on one machine reaches the others in about a second.
- **Just ssh:** it uses the system `ssh`, with your config, keys, agent and
  `ProxyJump`. Nothing listens on the network.
- **Peer to peer:** every machine is equal. Changes hop through other
  machines when two can't reach each other.
- **Offline-friendly:** edits made while disconnected catch up when the link
  returns.
- **Merges, not clobbers:** concurrent edits to different lines of a text
  file merge automatically. When they can't merge, nothing is lost: the
  other version is kept next to the file as a conflict copy.
- **Safe deletes:** a synced delete moves the file to a trash you can
  restore from for 30 days.
- **One version everywhere:** upgrade one machine and it upgrades the rest
  over the same links.
- **Groups:** share some files with all your machines and others with just
  a few. A machine outside a group never even sees those paths.

> **Beta.** Enigma is in daily use, but young. Your files are safe from it
> (deletes go to a trash, conflicts keep both versions), yet expect rough
> edges and report them.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/nadavushka-dev/enigma/main/install.sh | sh
```

It picks the release for your OS and CPU, checks its checksum and installs
`enigma` to `~/.local/bin`. Pin a version with `ENIGMA_VERSION=v0.1.0`.
macOS and Linux, amd64 and arm64.

Or from source, with Go 1.26+: `make build` writes `bin/enigma`, stamped
with the commit time so that a newer build upgrades the rest of the fleet.

## Quick start

```sh
enigma init                    # this machine starts a fleet
enigma service install         # run the daemon at login (launchd / systemd)
enigma machine add devbox      # any host in your ~/.ssh/config
enigma add ~/.claude           # sync a folder...
enigma add ~/.zshrc            # ...or a single file
enigma status
```

`machine add` does the rest on the other machine. It copies the right
binary over ssh, joins it to the fleet, starts its daemon and checks which
machines it can reach. Every step prints the exact command it runs.

## Groups

Every file you add goes to the group `main`, which every machine is in.
For files that only some machines should have, make a group:

```sh
enigma group create pair laptop devbox   # a group of two
enigma add --group pair ~/.ssh/config.d  # synced on laptop and devbox only
enigma group add pair nas                # nas joins: it gets the files
enigma group remove main nas             # nas leaves main: it keeps its files
enigma group delete pair                 # pair is gone; its entries stop syncing, every machine keeps its files
enigma entry move ~/.zshrc pair          # move an entry between groups
enigma group merge pair main             # fold a group into another
```

A machine that is not in a group never receives its files, and never
learns their names. Leaving a group stops syncing there; the files stay.
`enigma group list` shows every group, its machines and how many entries
it holds.

## The TUI

Run `enigma` on its own for a live view. It shows your machines and the
hosts in your ssh config, your groups, a browser over `~` with what's
synced, and an activity feed. Add machines and files from there with `a`,
manage groups with `n`, `enter` and `M`, press `enter` on a conflict to
compare the two versions side by side, `p` for what is wrong right now, and
`L` to change the limits.

<div align="center">
<img src="demo/tui.gif" alt="the Enigma TUI: machines, files and the activity feed" width="900">
</div>

## Commands

| Command | What it does |
|---|---|
| `enigma init [--name NAME]` | Start a fleet with this machine as its first member. |
| `enigma machine add <ssh-alias> [--group G]…` | Add a machine, to `main` or to the groups given. |
| `enigma machine list` · `remove <name> [--uninstall]` · `rename <name> <new>` · `alias <name> <ssh-alias>` | Manage the machines in the fleet. |
| `enigma add <path> [--group G] [--ignore GLOB]…` | Sync a file or folder on the machines of a group (default `main`). |
| `enigma remove <path>` | Stop syncing it. The files stay everywhere. |
| `enigma list` · `status` | What's synced, and the state of each link. Status starts with what is wrong right now. |
| `enigma group list` · `create <name> [machine]…` · `delete` · `rename` · `add <name> <machine>…` · `remove <name> <machine>…` · `merge <from> <into>` | Groups: which machines sync which entries. |
| `enigma entry move <path> <group>` | Move a synced file or folder to another group. |
| `enigma limits [--file-size SIZE] [--files N]` | Show the fleet's limits, or change them on every machine. |
| `enigma conflicts [--local]` | Conflict copies on every machine. |
| `enigma trash list` · `restore <id>` · `empty` | Files removed here by a synced delete. |
| `enigma doctor` | Check this machine and the links between all machines. |
| `enigma service install` / `uninstall` | Run the daemon at login. |
| `enigma update [--check]` | Move to the latest release. The fleet follows. |
| `enigma tui`, or plain `enigma` | The terminal UI. |

Every command takes `--json`, for scripts and agents.

## What it syncs, and what it doesn't

- **Files and folders under `~`**, with the exec bit. Only regular files:
  symlinks are listed in status and skipped.
- **Small things.** By default the limits are 10 MB per file and 2,000
  files per folder. Enigma is for the files around your projects, not the
  projects themselves. Change them for the whole fleet with
  `enigma limits --file-size 25MB --files 5000` (at most 64 MB and 20,000).
  A folder that grows past its limit keeps syncing what it has, and status
  shows the new files left out until there is room.
- **Always ignored:** `.git/`, `node_modules/`, `.DS_Store`, editor swap
  files. Add your own with `--ignore`.
- **When you add a folder** that already exists on another machine, the
  other machine backs up its copy first. Files that exist only on the other
  machine are kept and spread to everyone.

**Every member is trusted.** Machines upgrade each other over their links,
so a member can run a new Enigma binary on any machine it links to. Add
only machines you control, the same ones you would give ssh access to.

Enigma is not Git. It has no history to browse and no branches. It keeps
the last versions only to merge with.

## When something is off

- `enigma status` starts with the standing problems: a link that is down
  and why, a machine that cannot be upgraded, an entry missing locally or
  over its limit, a conflict copy from the last hour. Each comes with the
  command that fixes it. The TUI shows the same on its notice line, and `p`
  lists them.
- `enigma doctor` asks every machine over ssh and prints the connectivity
  matrix, versions and service state. It exits non-zero on problems.
- The daemon log is `~/.local/share/enigma/enigma.log` (rotated at 10 MB);
  a background daemon's stderr goes to `daemon.out` next to it. On macOS
  with `XDG_DATA_HOME` unset the folder is the same.
- Nothing is ever lost silently: an overwritten first add is in
  `~/.local/share/enigma/backups/`, a synced delete in the trash
  (`enigma trash list`), the other side of a conflict next to the file.
- Found a bug? Open an issue at
  <https://github.com/nadavushka-dev/enigma/issues> with the output of
  `enigma doctor` and the relevant lines of the log.

## How it works

Each machine runs `enigma daemon`. To link to another machine, a daemon runs
`ssh <host> enigma bridge`. The bridge just pipes that ssh session into the
other daemon's socket. A change is announced on every link, fetched by the
machines that don't have it yet, written atomically and passed on. Version
vectors tell a newer edit from a concurrent one. Concurrent text edits get a
three-way merge against their common ancestor, and both machines reach the
same result independently.

The full story, with diagrams, is in [`architecture/`](architecture/README.md):

<div align="center">
<a href="architecture/README.md"><img src="architecture/01-topology.svg" alt="machines, daemons and ssh links" width="800"></a>
</div>

The design of record is [`SPEC.md`](SPEC.md).

## Development

```sh
make check        # gofmt, go vet, go test -race (what the pre-commit hook runs)
make e2e          # three Linux containers with sshd, over real ssh (needs Docker)
make dist         # binaries for every platform, for machine add before a release
make diagrams     # render architecture/*.d2
sh demo/record.sh # re-record the GIFs above (Docker + vhs)
```

Every feature and every fix starts with a failing test. The suite includes
multi-machine simulations, a CLI end-to-end run over a fake ssh, and a Docker
suite over real ssh: network cuts, merges, and upgrades that hop from machine
to machine.

## License

MIT, see [`LICENSE`](LICENSE).

# Safety and recovery

Enigma is forgiving by design, but some commands change every machine. Ask
before acting, show the plan, and verify after.

## Destructive actions

| Command | What stays | What changes |
|---|---|---|
| `enigma remove <path>` | Files on every machine | Stops syncing that entry everywhere. |
| `enigma group delete <name>` | Files on every machine | Stops syncing the group's entries everywhere. Removed records are tombstones. |
| `enigma group merge <from> <into>` | Files on every machine | Moves every entry, then deletes `<from>`; members only of `<from>` may lose entries unless `--add-members`. |
| `enigma machine remove <name>` | Files on that machine | The machine leaves the fleet and stops linking. |
| `enigma machine remove <name> --uninstall` | Files on that machine | Also removes its service and binary. |
| `enigma trash empty` | Nothing | Permanently deletes this machine's trash. |
| `enigma trash restore <id>` | — | Restores the file here; if it is synced, it reaches every machine as a normal edit. Restore on one machine, then verify; restoring on several can conflict. |

`remove`, `group delete` and `group merge` keep the files. That is the safe
way to stop sharing something. Do not delete files on one machine to make
them disappear everywhere; a synced delete moves each machine's copy to its
own trash.

Before `--yes`, show the plan (`group delete`/`group merge` print it) and get
the user's confirmation. Never pass `--yes` "to save a prompt" on a fleet you
do not own.

## Conflicts

A conflict copy is named
`<stem>.conflict-<machine>-<YYYYMMDD-HHMMSS>-<hash6><ext>` next to the
original. Both versions are intact:

- inside a folder entry the copy syncs to every member;
- beside a single-file entry it exists only on the machines that resolved the
  conflict (the adder and the machine that lost).

To resolve:

1. `enigma conflicts --json` and read the copy.
2. Compare the original and the copy; decide what the file should say.
3. Edit the original.
4. Delete the copy on every machine that holds it (`conflicts --json` lists
   `on`).
5. Verify `enigma status --json`.

Do not delete either version before deciding; the copy is often the only
place the other edit exists.

## Trash

`enigma trash list --json` shows `id`, `path`, `day`, `size`, `mtime`. The
trash is **per machine**: a synced delete moves each machine's copy to that
machine's trash. Restore the one you need; it syncs as an edit. Restoring on
several machines at once can create a conflict.

`enigma trash empty` is permanent and local. Ask first.

## Live fleets and upgrades

- A newer Enigma binary upgrades every machine over the links. Do not run a
  dev build (`make build`) against a live fleet unless the user wants the
  whole fleet upgraded.
- Version mismatches show as link problems; the fleet upgrades itself and
  the link returns. Do not try to force a sync while versions differ.
- `enigma update` moves this machine to the latest release and restarts the
  daemon; the fleet follows. Ask first.

## State you must not edit

- `~/.local/share/enigma/state.json` — fleet state and current versions.
- `~/.local/share/enigma/identity.json` — this machine's ID, fleet and name.
- `~/.local/share/enigma/history/` — merge bases.
- `~/.local/share/enigma/trash/`, `backups/` — recovery data.
- `~/.local/share/enigma/enigma.sock`, `peer.sock` — live sockets.

If the daemon is wedged, read the log and use `status`/`doctor`; do not delete
state to "reset" syncing.

## Recovery map

| Problem | Where the data is |
|---|---|
| Overwritten on first add (mine-wins) | The losing machine's `backups/<date>/<path>.<timestamp>` |
| Deleted by a synced delete | Each machine's `trash/<day>/<path>` |
| Concurrent edit could not merge | The `.conflict-` copy next to the file |
| First-add keep-both | A `.conflict-` copy; inside a folder it syncs, beside a single file it stays on the machines that resolved it |
| Entry root vanished | `status` `Missing`; deletes are suppressed until the root is back |

Nothing is deleted silently by Enigma itself.

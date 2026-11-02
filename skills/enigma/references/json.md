# JSON shapes

Use `--json` for reads. The commands below are the ones with JSON output;
`enigma list` is human only — use `enigma status --json` for entries.

## add: preview and result

```json
{
  "path": "~/test.txt",
  "action": "dry-run",
  "policy": "",
  "report": {
    "path": "test.txt",
    "dir": false,
    "group": "main",
    "group_name": "main",
    "local": {
      "id": "id-s2",
      "name": "s2",
      "local": true,
      "connected": true,
      "in_group": true,
      "exists": true,
      "files": [{"path": "", "hash": "…", "size": 8, "mtime": 0}]
    },
    "machines": [
      {"id": "id-s2", "name": "s2", "local": true, "connected": true, "in_group": true, "exists": true},
      {"id": "id-s1", "name": "s1", "connected": true, "in_group": true, "exists": true, "files": [{"path": "", "hash": "…", "size": 4, "mtime": 0}]}
    ],
    "files": [
      {"path": "", "state": "different", "machines": ["s1"]}
    ],
    "summary": {
      "files": 1,
      "same": 0,
      "different": 1,
      "only_here": 0,
      "only_there": 0,
      "unknown": 0,
      "truncated": 0
    }
  }
}
```

- `action`: `dry-run`, `added`, or `cancelled`.
- `policy`: `keep-both` or `replace` after an add.
- `report.files[].state`: `different`, `only-here`, or `only-there`.
- `report.files[].machines`: peer names where the file differs or exists.
- `report.summary.unknown`: machines that did not answer; the policy applies
  when they reconnect.
- `report.summary.truncated`: too many files to compare on a peer; only
  keep-both is fully safe.
- For a folder, `files[].path` is relative to the entry root. For a single
  file it is `""`.

## status

```json
{
  "machine": "s2",
  "machine_id": "id-s2",
  "fleet": "…",
  "version": "v0.4.0",
  "entries": [
    {
      "Path": "notes",
      "Dir": true,
      "Group": "main",
      "Frozen": false,
      "Missing": false,
      "Skipped": null,
      "OverLimit": null
    }
  ],
  "peers": [
    {"name": "s1", "id": "id-s1", "target": "ssh://s1", "connected": true, "last_error": ""}
  ],
  "files": 12,
  "conflicts": ["notes/a.conflict-s1-20261102-132319-1a2b3c.md"],
  "limits": {"max_file_size": 10485760, "max_files": 2000},
  "problems": [
    {"key": "link:s1", "severity": "error", "since": "2026-11-02T15:00:00Z",
     "message": "s1 link down since 15:00", "hint": "check ssh s1", "peer": "s1"}
  ]
}
```

- The nested entry keys are **capitalized** (`Path`, `Dir`, `Group`, `Frozen`,
  `Missing`, `Skipped`, `OverLimit`); the rest are lowercase.
- `problems` is the standing set, errors first. Quote the `message` and
  `hint`.
- `conflicts` is a list of conflict-copy paths on this machine.

## conflicts

```json
[
  {"path": "notes/a.conflict-s1-20261102-132319-1a2b3c.md",
   "original": "notes/a.md", "machine": "s1",
   "time": "2026-11-02T13:23:19Z", "on": ["s1", "s2"]}
]
```

`--local` restricts it to this machine; without it, Enigma asks every member.

## trash list

```json
[
  {"id": "2026-11-02/notes/a.md", "path": "notes/a.md",
   "day": "2026-11-02T00:00:00Z", "size": 42, "mtime": "…"}
]
```

## doctor

```json
{
  "checks": ["service installed: …"],
  "machines": [
    {"name": "s1", "version": "v0.4.0", "links": {"s2": true}}
  ],
  "problems": [],
  "warnings": []
}
```

A machine that could not be asked has `error` instead of `version`/`links`.
`doctor` exits non-zero when `problems` is non-empty.

## groups, machines, limits

`group list --json`:

```json
[
  {"id": "main", "name": "main", "all": true, "except": null,
   "members": ["id-s1", "id-s2"], "member_names": ["s1", "s2"], "entries": 4,
   "clock": 12, "by": "id-s1"}
]
```

`machine list --json`:

```json
[
  {"id": "id-s1", "name": "s1", "alias": "s1", "target": "ssh://s1",
   "bin": "/home/dev/.local/bin/enigma", "clock": 3, "by": "id-s1"}
]
```

`limits --json`:

```json
{"max_file_size": 10485760, "max_files": 2000}
```

# Enigma

Real-time sync of chosen files and folders between machines over ssh, written in Go.
**`SPEC.md` is the design of record.** Read it before writing code. The original brief is `INITIAL_PROMPT.md`.

## Rules
- **TDD, always** (SPEC §13): write a failing test first, then the code. Every bug fix starts with a test that reproduces it.
- Never delete, skip or weaken an existing test to make a change pass. Raise it and ask.
- Discuss design changes before implementing them. If code and SPEC disagree, stop and ask.
- `go test -race ./...`, `gofmt -l .` and `go vet ./...` must be clean before anything is called done.
- Dependencies: stdlib, fsnotify, BurntSushi/toml, bubbletea/bubbles/lipgloss. Ask before adding anything else.
- Enigma is standalone: no coupling to other tools. It talks to other machines only through the system `ssh`.
  The one optional exception: `enigma add` with no path uses `fzf` if it is installed (SPEC §12.4).

## Dev workflow (not part of the product)
- Tasks: tuxedo, `TODO_FILE=$PWD/todo.txt tuxedo ...`
- Review: `tuicr -w` before committing.
- Parallel agent work: herdr workspaces.

# Hand tests (before every release)

## Purpose and order

Automated tests prove what we thought of. This checklist is run by hand,
in the sandbox, to find what we did not. It is run by a **separate
validator agent**: not the implementer, not the reviewer.

Release order:

1. implement
2. automated tests (`make check`, `make e2e`)
3. independent review
4. **hand test in the sandbox (this file)**
5. install

Rule: every bug found by hand becomes **(a)** a scenario here and
**(b)** an automated test once it is fixed (engine test, plus the Docker
e2e when it needs real ssh or a real Linux file system).

## How to run

- Start: `make sandbox` (three Linux boxes s1, s2, s3 running this tree,
  linked over ssh; `HOME=/home/dev`).
- A shell: `e2e/sandbox.sh shell sN`. One command: `e2e/sandbox.sh run sN enigma status`.
- Cut a box off the network, and back:
  `docker network disconnect enigma-sandbox-net enigma-sandbox-sN`
  `docker network connect --alias sN enigma-sandbox-net enigma-sandbox-sN`
- Restart a daemon: kill the daemon's PID (`pgrep -f 'enigma daemon'`,
  then `kill <pid>`; the log says "daemon stopped"), then
  `enigma service install`. `service install` alone does **not** restart a
  daemon that is running.
- Look: `enigma status` (problems first), `enigma status --json`,
  `enigma trash list`, `enigma log`.
- Stop: `make sandbox-down`.

## Scenarios

Each: steps, then what must be true. "Peers" means the other two boxes.

1. **A link to a synced target.** On s1: `mkdir -p ~/repo/skills/foo`,
   `echo x > ~/repo/skills/foo/SKILL.md`, `enigma add ~/repo/skills`,
   `mkdir -p ~/.agents/skills`, `ln -s ~/repo/skills/foo ~/.agents/skills/foo`,
   `enigma add --links ~/.agents/skills`.
   Expect: on the peers `readlink ~/.agents/skills/foo` is their own
   `/home/dev/repo/skills/foo`, and `cat ~/.agents/skills/foo/SKILL.md` works.
2. **Changes through it.** Edit `~/repo/skills/foo/SKILL.md` on s1; add a
   second skill and its link. Expect: both reach the peers within seconds;
   the link files are links on every box, not copies.
3. **Links that must not travel.** In `~/.agents/skills` on s1:
   `ln -s /etc etc`, `ln -s ~/nowhere gone`, `ln -s .. up`.
   Expect: `enigma status` on s1 lists each as skipped with a reason
   (outside your home, not here, a loop); none appears on the peers.
4. **Link vs real folder.** Cut s2 off. On s1 add a link
   `~/.agents/skills/bar`; on s2 create a real folder
   `~/.agents/skills/bar/SKILL.md`. Reconnect.
   Expect: s2 reports the link **blocked** (an error), its folder is intact;
   s1's link is intact and nothing was written into its target.
5. **Delete a link.** On s1 `rm ~/.agents/skills/foo`.
   Expect: the link is in the peers' `enigma trash list`; every
   `~/repo/skills/foo/SKILL.md` is untouched.
6. **REGRESSION (found by hand 2026-11-11): entry root replaced, removed,
   re-created, restart.** Links off. On s3: `mkdir -p ~/notes`,
   `echo n1 > ~/notes/a.md`, `echo n2 > ~/notes/b.md`, `enigma add ~/notes`.
   Then on one line: `mkdir -p ~/repo && cp -r ~/notes ~/repo/notes && mv ~/notes ~/notes.old && ln -s ~/repo/notes ~/notes`.
   Then `rm ~/notes; rm -rf ~/notes.old`, then `mkdir ~/notes`, then
   restart s3's daemon.
   Expect at every step: s1 and s2 keep both files and their trash stays
   empty; while the root is a link or gone, s3's status says the entry is
   missing locally and `~/notes` is not re-created; after `mkdir ~/notes`
   both files come back on s3 within seconds (status shows "restoring"
   until then); after the restart, still no delete anywhere.

### Always also try

- Restart a daemon after every scenario, and check nothing changed.
- Disconnect and reconnect a peer in the middle of a scenario.
- Check `enigma trash list` on **every** box for anything unexpected.

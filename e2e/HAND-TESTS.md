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
  `enigma trash list`, and the log at `~/.local/share/enigma/enigma.log`
  (`tail -f` it; there is no `enigma log` command).
- Folder identity: `stat -c '%i %W' ~/notes` shows the inode and birth
  time; Linux often gives a re-created folder the old inode.
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
7. **A1 (found by hand 2026-11-11): root deleted and re-created on one
   line.** On s3 with `~/notes` synced: `rm -rf ~/notes && mkdir ~/notes`
   (check with `stat` that the inode came back).
   Expect: s1 and s2 keep every file, trash empty; s3 gets them back within
   seconds.
8. **A2b: root deleted, left gone, re-created later.** `rm -rf ~/notes`;
   wait 10 s (status: missing locally, deletes not sent); `mkdir ~/notes`.
   Expect: as in 7.
9. **A16: scenario 6 without the symlink.** `mv ~/notes ~/notes.old`;
   wait 5 s; `rm -rf ~/notes.old; mkdir ~/notes`.
   Expect: as in 7.
10. **F2: the re-created folder keeps syncing.** After 7, 8 or 9:
    `echo after > ~/notes/new.md`.
    Expect: `new.md` reaches the peers within seconds, without a restart;
    `enigma status` never shows the entry as plain `ok` while it is not
    watched (it says `NOT WATCHED` until it is).
11. **S2: following the "blocked" hint.** After 4, on s2
    `rm -rf ~/.agents/skills/bar`.
    Expect: within seconds the link appears on s2 (its own `$HOME`), the
    blocked error goes, no restart needed; s2's old `SKILL.md` is not
    listed as skipped anywhere afterwards (S3).
12. **S4: concurrent retargets.** Cut s2. On s1 retarget
    `~/.agents/skills/foo` to one skill; wait 2 s; on s2 retarget it to
    another. Reconnect.
    Expect: every box ends on s2's target; **every** box that had s1's
    link, s1 included, has it in `enigma trash list` and a "lost" warning;
    no `.conflict-` link anywhere.
13. **S5: links off.** `enigma entry links ~/.agents/skills off` on s1,
    then `rm ~/.agents/skills/foo` on s1.
    Expect: the peers keep their link (nothing in their trash). Turn links
    back on: the link comes back on s1 from the peers.
14. **Status stays true (S1, S6, S7).** After every scenario above,
    `enigma status` on each box lists only what is wrong now: no
    "skipped" for links that are gone, no "is a symlink" or "vanished"
    for folders that are back, no conflict whose copy was deleted, no line
    twice. A box outside an entry's group shows it as "not synced here",
    never `ok`. `enigma add` that you cancel exits non-zero (`echo $?`).

### Always also try

- Restart a daemon after every scenario, and check nothing changed.
- Disconnect and reconnect a peer in the middle of a scenario.
- Check `enigma trash list` on **every** box for anything unexpected.

#!/bin/sh
# Re-records the README GIFs: stages the demo, runs the tapes, cleans up.
set -eu
cd "$(dirname "$0")/.."
sh demo/setup.sh
mkdir -p /tmp/enigma-demo/home/.claude
printf '# House rules\n- be concise\n' > /tmp/enigma-demo/home/.claude/CLAUDE.md
mkdir -p /tmp/enigma-demo/home/work/notes
printf 'client list, rates, invoices\n' > /tmp/enigma-demo/home/work/notes/clients.md
# links.tape: a repo of agent skills and the flat folder that links into it
P=/tmp/enigma-demo/home/plugs S=/tmp/enigma-demo/home/.agents/skills
mkdir -p "$P/squad/skills/tkt-work" "$P/toolkit/skills/pin-board" "$P/toolkit/skills/standup" "$S"
printf -- '---\nname: tkt-work\ndescription: work a ticket end to end\n---\n- read the ticket, then the code\n' > "$P/squad/skills/tkt-work/SKILL.md"
printf -- '---\nname: pin-board\ndescription: pin notes for the session\n---\n' > "$P/toolkit/skills/pin-board/SKILL.md"
printf -- '---\nname: standup\ndescription: write the daily standup\n---\n' > "$P/toolkit/skills/standup/SKILL.md"
ln -s "$P/squad/skills/tkt-work" "$P/toolkit/skills/pin-board" /etc "$S/"
vhs demo/sync.tape
vhs demo/links.tape
( sleep 9; /tmp/enigma-demo/bin/ssh devbox 'echo "- small commits" >> .claude/CLAUDE.md' ) &
vhs demo/tui.tape
sh demo/teardown.sh

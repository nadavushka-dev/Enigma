#!/bin/sh
# Re-records the README GIFs: stages the demo, runs the tapes, cleans up.
set -eu
cd "$(dirname "$0")/.."
sh demo/setup.sh
mkdir -p /tmp/enigma-demo/home/.claude
printf '# House rules\n- be concise\n' > /tmp/enigma-demo/home/.claude/CLAUDE.md
mkdir -p /tmp/enigma-demo/home/work/notes
printf 'client list, rates, invoices\n' > /tmp/enigma-demo/home/work/notes/clients.md
vhs demo/sync.tape
( sleep 9; /tmp/enigma-demo/bin/ssh devbox 'echo "- small commits" >> .claude/CLAUDE.md' ) &
vhs demo/tui.tape
sh demo/teardown.sh

# Recorded by demo/record.sh (needs setup.sh's staging first).
Output demo/sync.gif
Set Shell bash
Set Theme "Catppuccin Mocha"
Set FontSize 17
Set Width 1600
Set Height 760
Set Padding 24
Set TypingSpeed 35ms
Set WindowBar Colorful

Hide
Type ". /tmp/enigma-demo/env && clear" Enter
Show

Type "# start a fleet on this machine" Enter
Type "enigma init --name mac" Enter
Sleep 1.5s
Hide
Type "enigma service install >/dev/null 2>&1 && clear" Enter
Show

Type "# add another machine: anything you can ssh to" Enter
Type "enigma machine add devbox" Enter
Sleep 7s
Hide
Type "enigma machine add nas >/dev/null 2>&1; clear" Enter
Sleep 7s
Show

Type "# sync a folder" Enter
Type "enigma add ~/.claude" Enter
Sleep 2s
Type "ssh devbox cat .claude/CLAUDE.md" Enter
Sleep 2s

Type "# edit it on devbox..." Enter
Type `ssh devbox 'echo "- tests first" >> .claude/CLAUDE.md'` Enter
Sleep 2.5s
Type "# ...and it's already here" Enter
Type "cat ~/.claude/CLAUDE.md" Enter
Sleep 2s

Type "clear" Enter
Type "# a group: files that only some machines share" Enter
Type "enigma group create pair mac devbox" Enter
Sleep 1.5s
Type "enigma add --group pair ~/work" Enter
Sleep 2.5s
Type "ssh devbox ls work/notes" Enter
Sleep 1.5s
Type "ssh nas ls work   # nas is not in pair: it never hears of it" Enter
Sleep 2s
Type "enigma group list" Enter
Sleep 4s

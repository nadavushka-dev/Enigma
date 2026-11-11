# Recorded by demo/record.sh, after sync.tape (record.sh stages ~/plugs
# and the links in ~/.agents/skills).
Output demo/links.gif
Set Shell bash
Set Theme "Catppuccin Mocha"
Set FontSize 14
Set Width 1720
Set Height 760
Set Padding 24
Set TypingSpeed 30ms
Set WindowBar Colorful

Hide
Type ". /tmp/enigma-demo/env && clear" Enter
Show

Type "# a repo of agent skills, and the flat folder agents read: one link per skill" Enter
Type "ls ~/.agents/skills" Enter
Sleep 1.2s
Type "readlink ~/.agents/skills/tkt-work" Enter
Sleep 1.5s

Type "# sync the repo, then the links" Enter
Type "enigma add ~/plugs" Enter
Sleep 1.5s
Type "enigma add --links ~/.agents/skills" Enter
Sleep 2s
Type "ssh devbox readlink .agents/skills/tkt-work   # under devbox's own home" Enter
Sleep 1.5s
Type "ssh devbox cat .agents/skills/tkt-work/SKILL.md" Enter
Sleep 2.5s

Type "clear" Enter
Type "# edit a skill in the repo, link a new one" Enter
Type `echo "- tests first" >> ~/plugs/squad/skills/tkt-work/SKILL.md` Enter
Type "ln -s ~/plugs/toolkit/skills/standup ~/.agents/skills/" Enter
Sleep 1.5s
Type "ssh devbox tail -1 .agents/skills/tkt-work/SKILL.md" Enter
Sleep 1.2s
Type "ssh devbox ls .agents/skills" Enter
Sleep 2s

Type "# each link and its state: a link outside ~ is skipped, and says why" Enter
Type "enigma status" Enter
Sleep 5s

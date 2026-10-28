# Recorded by demo/record.sh, after sync.tape.
Output demo/tui.gif
Set Shell bash
Set Theme "Catppuccin Mocha"
Set FontSize 16
Set Width 1300
Set Height 760
Set TypingSpeed 60ms
Set Padding 24
Set WindowBar Colorful

Hide
Type ". /tmp/enigma-demo/env && clear" Enter
Show

Type "enigma" Enter
Sleep 2.5s
Down Sleep 600ms Down Sleep 1.2s
Tab Sleep 1.2s          # Groups: main and pair
Down Sleep 800ms
Type "a" Sleep 1.5s     # pair is now where adds go
Tab Sleep 1s            # Files: "adding to pair"
Type "G" Sleep 800ms
Enter Sleep 1.5s        # open ~/work: synced in pair
Sleep 3s # record.sh edits the file on devbox meanwhile: watch Activity
Type "?"
Sleep 3s
Escape
Sleep 1s
Type "q"
Sleep 500ms

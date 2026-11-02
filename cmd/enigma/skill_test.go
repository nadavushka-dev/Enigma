package main

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// Regression for the shipped Agent Skill: every `enigma <command>` it
// mentions must still exist, so a command rename cannot leave the skill
// teaching agents a dead command.
func TestSkillMentionsOnlyRealCommands(t *testing.T) {
	files, err := filepath.Glob("../../skills/enigma/*.md")
	if err != nil {
		t.Fatal(err)
	}
	refs, err := filepath.Glob("../../skills/enigma/references/*.md")
	if err != nil {
		t.Fatal(err)
	}
	files = append(files, refs...)
	if len(files) == 0 {
		t.Fatal("no skill files found under skills/enigma")
	}
	// `help` is handled in run(), not registered as a command.
	meta := map[string]bool{"help": true}
	re := regexp.MustCompile(`enigma ([a-z][a-z-]*)`)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			name := m[1]
			if _, ok := commands[name]; !ok && !meta[name] {
				t.Errorf("%s mentions `enigma %s`, which is not a command", f, name)
			}
		}
	}
}

package engine

import (
	"fmt"

	"github.com/nadavushka-dev/enigma/internal/proto"
)

// SetMerge sets how concurrent edits are merged for the entry at abs, on
// every machine of its group (SPEC §7 step 3). newestBy needs json.
func (e *Engine) SetMerge(abs string, mode proto.MergeMode, newestBy string) error {
	if mode == proto.MergeLines {
		mode = ""
	}
	switch {
	case mode != "" && mode != proto.MergeJSON:
		return fmt.Errorf("unknown merge mode %q; use lines or json", mode)
	case newestBy != "" && mode != proto.MergeJSON:
		return fmt.Errorf("--newest-by needs the json merge")
	}
	var err error
	if derr := e.do(func() {
		rel, ok := e.rel(abs)
		r, exists := e.st.Entries[rel]
		switch {
		case !ok || !exists || r.Removed:
			err = fmt.Errorf("%s is not synced", abs)
		case !e.inScope(r):
			err = fmt.Errorf("%s belongs to group %s, which this machine is not in", abs, e.groupName(r.Group))
		case r.Merge == mode && r.MergeNewestBy == newestBy:
		default:
			e.st.Lamport++
			r.Clock, r.By, r.Merge, r.MergeNewestBy = e.st.Lamport, e.o.MachineID, mode, newestBy
			e.applyEntry(r, nil)
		}
	}); derr != nil {
		return derr
	}
	return err
}

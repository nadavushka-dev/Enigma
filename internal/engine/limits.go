package engine

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/nadavushka-dev/enigma/internal/proto"
)

// Ceilings for the group's limits (SPEC §8): a file travels as one message,
// and a folder past a few thousand files is a project, not a dotfile.
const (
	CeilingFileSize = proto.MaxBody
	CeilingFiles    = 20000
)

// Limits are the group's limits (SPEC §8).
type Limits struct {
	MaxFileSize int64 `json:"max_file_size"`
	MaxFiles    int   `json:"max_files"`
}

func (l Limits) String() string {
	return fmt.Sprintf("%s per file, %s files per folder", FormatSize(l.MaxFileSize), FormatCount(l.MaxFiles))
}

// lim is the limits in force; loop only.
func (e *Engine) lim() Limits {
	if s := e.st.Settings; s != nil {
		return Limits{MaxFileSize: s.MaxFileSize, MaxFiles: s.MaxFiles}
	}
	return Limits{MaxFileSize: e.o.MaxFileSize, MaxFiles: e.o.MaxFiles}
}

// Limits returns the group's limits.
func (e *Engine) Limits() Limits {
	var l Limits
	e.do(func() { l = e.lim() })
	return l
}

// SetLimits changes the group's limits for every machine. A zero field
// keeps its value.
func (e *Engine) SetLimits(l Limits) error {
	switch {
	case l.MaxFileSize < 0:
		return fmt.Errorf("the file size must be positive")
	case l.MaxFiles < 0:
		return fmt.Errorf("the file count must be positive")
	case l.MaxFileSize > CeilingFileSize:
		return fmt.Errorf("a file can be at most %s: it travels as one message", FormatSize(CeilingFileSize))
	case l.MaxFiles > CeilingFiles:
		return fmt.Errorf("a folder can hold at most %s files: Enigma is not for whole projects", FormatCount(CeilingFiles))
	}
	return e.do(func() {
		cur := e.lim()
		if l.MaxFileSize == 0 {
			l.MaxFileSize = cur.MaxFileSize
		}
		if l.MaxFiles == 0 {
			l.MaxFiles = cur.MaxFiles
		}
		e.st.Lamport++
		e.applySettings(proto.SettingsRecord{MaxFileSize: l.MaxFileSize, MaxFiles: l.MaxFiles, Clock: e.st.Lamport, By: e.o.MachineID}, nil)
	})
}

// applySettings merges a record (local when from is nil) into group state.
// New limits rescan every entry: files may now fit, or no longer.
func (e *Engine) applySettings(r proto.SettingsRecord, from *peer) {
	old := e.st.Settings
	if old != nil && !r.Newer(*old) {
		return
	}
	if r.MaxFileSize <= 0 || r.MaxFiles <= 0 {
		e.log.Warn("settings record without limits; ignored", "by", r.By)
		return
	}
	if r.Clock > e.st.Lamport {
		e.st.Lamport = r.Clock
	}
	before := e.lim()
	e.st.Settings = &r
	e.dirty = true
	e.broadcast(&proto.Msg{Type: proto.Settings, Settings: &r}, from)
	if now := e.lim(); now != before {
		by := ""
		if from != nil {
			by = from.name
		}
		e.log.Info("limits changed", "limits", now.String())
		e.event(EvLimits, "", by, now.String())
		e.rescanAll()
	}
}

// FormatSize writes n bytes for people: 8 B, 512 KB, 10 MB, 2.5 MB.
func FormatSize(n int64) string {
	switch {
	case n < 1<<10:
		return fmt.Sprintf("%d B", n)
	case n < 1<<20:
		return trimUnit(float64(n)/(1<<10), "KB")
	case n < 1<<30:
		return trimUnit(float64(n)/(1<<20), "MB")
	}
	return trimUnit(float64(n)/(1<<30), "GB")
}

// trimUnit writes v with one decimal, dropping a ".0".
func trimUnit(v float64, unit string) string {
	return strings.TrimSuffix(strconv.FormatFloat(v, 'f', 1, 64), ".0") + " " + unit
}

// FormatCount writes n with thousands separators: 20,000.
func FormatCount(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0 && s[i-1] != '-'; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// ParseSize reads a size like 25MB, 500K or 1.5 MB (binary units; a bare
// number is bytes).
func ParseSize(s string) (int64, error) {
	t := strings.ToUpper(strings.ReplaceAll(s, " ", ""))
	units := []struct {
		suffix string
		mult   float64
	}{{"GB", 1 << 30}, {"G", 1 << 30}, {"MB", 1 << 20}, {"M", 1 << 20}, {"KB", 1 << 10}, {"K", 1 << 10}, {"B", 1}}
	mult := 1.0
	for _, u := range units {
		if strings.HasSuffix(t, u.suffix) {
			t, mult = strings.TrimSuffix(t, u.suffix), u.mult
			break
		}
	}
	v, err := strconv.ParseFloat(t, 64)
	if err != nil || v <= 0 {
		return 0, errors.New("wants a size like 25MB, not " + strconv.Quote(s))
	}
	return int64(v * mult), nil
}

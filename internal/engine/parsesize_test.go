package engine

import "testing"

func TestParseSize(t *testing.T) {
	for in, want := range map[string]int64{
		"1024": 1024, "25MB": 25 << 20, "25M": 25 << 20, "25mb": 25 << 20, "500KB": 500 << 10,
		"500k": 500 << 10, "1.5MB": 3 << 19, "1GB": 1 << 30, "8B": 8, " 10 MB ": 10 << 20,
	} {
		if got, err := ParseSize(in); err != nil || got != want {
			t.Errorf("ParseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "abc", "-5MB", "0", "10XB", "MB"} {
		if got, err := ParseSize(in); err == nil {
			t.Errorf("ParseSize(%q) = %d, want an error", in, got)
		}
	}
}

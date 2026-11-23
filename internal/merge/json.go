package merge

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"sort"
	"strings"
)

// JSON3 merges a and b, two edits of base, as JSON objects (SPEC §7 step 3,
// entries with merge json). Each top-level key is one unit: a key changed,
// added or removed on one side only takes that side's value; a key changed
// differently on both sides is a conflict (ok false). Values are never
// merged inside, so the fields of one key always come from one side.
//
// isJSON is false when any of the three is not one JSON object (or repeats a
// key); the caller then merges lines instead. The result does not depend on
// which side is a, and its layout comes from base only, so two machines
// merging the same pair make the same bytes.
//
// newestBy, when set, ranks a key changed on both sides: if both values are
// objects whose newestBy field is a number, the larger one wins whole. A
// tie, a missing or non-number field, or a removed key stays a conflict.
func JSON3(base, a, b []byte, newestBy string) (out []byte, isJSON, ok bool) {
	bo, err := parseObject(base)
	if err != nil {
		return nil, false, false
	}
	ao, err := parseObject(a)
	if err != nil {
		return nil, false, false
	}
	co, err := parseObject(b)
	if err != nil {
		return nil, false, false
	}
	merged := map[string]string{}
	for _, k := range unionKeys(bo, ao, co) {
		bv, inB := bo.vals[k]
		av, inA := ao.vals[k]
		cv, inC := co.vals[k]
		switch {
		case inA == inC && av == cv: // same on both sides
		case inA == inB && av == bv: // only b changed it
			av, inA = cv, inC
		case inC == inB && cv == bv: // only a changed it
		case newestBy != "" && inA && inC:
			switch newer(av, cv, newestBy) {
			case 1:
			case -1:
				av = cv
			default:
				return nil, true, false
			}
		default:
			return nil, true, false
		}
		if inA {
			merged[k] = av
		}
	}
	// Order: the base's keys that remain, then new keys by name.
	var keys, added []string
	for _, k := range bo.order {
		if _, ok := merged[k]; ok {
			keys = append(keys, k)
		}
	}
	for k := range merged {
		if _, ok := bo.vals[k]; !ok {
			added = append(added, k)
		}
	}
	sort.Strings(added)
	keys = append(keys, added...)
	return render(keys, merged, layoutOf(base)), true, true
}

// newer compares the field of two object values: 1 if a's is larger, -1 if
// b's, 0 if they tie or either cannot be ranked.
func newer(a, b, field string) int {
	x, okA := numberField(a, field)
	y, okB := numberField(b, field)
	if !okA || !okB {
		return 0
	}
	return x.Cmp(y)
}

func numberField(v, field string) (*big.Float, bool) {
	var o map[string]json.RawMessage
	if json.Unmarshal([]byte(v), &o) != nil || o == nil {
		return nil, false
	}
	raw, ok := o[field]
	if !ok || len(raw) == 0 || raw[0] == '"' { // json.Number takes "9" too
		return nil, false
	}
	var n json.Number
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if dec.Decode(&n) != nil {
		return nil, false
	}
	f, ok := new(big.Float).SetPrec(256).SetString(n.String())
	return f, ok
}

type object struct {
	order []string
	vals  map[string]string // compacted JSON, so layout is not a change
}

var errNotObject = errors.New("not one JSON object")

func parseObject(b []byte) (object, error) {
	o := object{vals: map[string]string{}}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return o, errNotObject
	}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return o, err
		}
		k, _ := t.(string)
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return o, err
		}
		if _, dup := o.vals[k]; dup {
			return o, errNotObject
		}
		var c bytes.Buffer
		if err := json.Compact(&c, raw); err != nil {
			return o, err
		}
		o.order = append(o.order, k)
		o.vals[k] = c.String()
	}
	if t, err := dec.Token(); err != nil || t != json.Delim('}') {
		return o, errNotObject
	}
	if _, err := dec.Token(); err != io.EOF {
		return o, errNotObject
	}
	return o, nil
}

func unionKeys(os ...object) []string {
	seen := map[string]bool{}
	var keys []string
	for _, o := range os {
		for _, k := range o.order {
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}
	return keys
}

type layout struct {
	indent  string // "" is one line
	newline bool   // a newline after the closing brace
}

// layoutOf reads the indentation of the first key line and the final newline.
func layoutOf(b []byte) layout {
	l := layout{newline: bytes.HasSuffix(b, []byte("\n"))}
	body := strings.TrimRight(string(b), " \t\r\n")
	lines := strings.Split(body, "\n")
	for _, line := range lines[1:] {
		if t := strings.TrimLeft(line, " \t"); t != "" && t != "}" {
			l.indent = line[:len(line)-len(t)]
			if l.indent == "" {
				l.indent = "  "
			}
			break
		}
	}
	return l
}

func render(keys []string, vals map[string]string, l layout) []byte {
	var out bytes.Buffer
	out.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			out.WriteByte(',')
		}
		if l.indent != "" {
			out.WriteString("\n" + l.indent)
		}
		out.Write(quote(k))
		out.WriteByte(':')
		if l.indent == "" {
			out.WriteString(vals[k])
			continue
		}
		out.WriteByte(' ')
		json.Indent(&out, []byte(vals[k]), l.indent, l.indent) // valid: it was compacted
	}
	if l.indent != "" && len(keys) > 0 {
		out.WriteByte('\n')
	}
	out.WriteByte('}')
	if l.newline {
		out.WriteByte('\n')
	}
	return out.Bytes()
}

func quote(s string) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.Encode(s) // a string always encodes
	return bytes.TrimSuffix(b.Bytes(), []byte("\n"))
}

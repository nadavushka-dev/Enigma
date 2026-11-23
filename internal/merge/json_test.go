package merge

import "testing"

// Pi's auth.json shape: 2-space indent, no trailing newline.
const jsonBase = `{
  "a": {
    "token": "a0",
    "expires": 1
  },
  "b": {
    "token": "b0",
    "expires": 1
  }
}`

func TestJSON3(t *testing.T) {
	for _, tc := range []struct {
		name, base, a, b string
		want             string // "" means a conflict
		notJSON          bool
	}{
		{name: "different keys changed merge, even when the lines touch",
			base: jsonBase,
			a:    `{"a": {"token": "a1", "expires": 2}, "b": {"token": "b0", "expires": 1}}`,
			b:    `{"a": {"token": "a0", "expires": 1}, "b": {"token": "b1", "expires": 2}}`,
			want: "{\n  \"a\": {\n    \"token\": \"a1\",\n    \"expires\": 2\n  },\n  \"b\": {\n    \"token\": \"b1\",\n    \"expires\": 2\n  }\n}"},
		{name: "the same key changed differently is a conflict, never a mix",
			base: jsonBase,
			a:    `{"a": {"token": "a1", "expires": 2}, "b": {"token": "b0", "expires": 1}}`,
			b:    `{"a": {"token": "a0", "expires": 3}, "b": {"token": "b0", "expires": 1}}`},
		{name: "the same change on both sides is no conflict",
			base: `{"a": 1, "b": 1}`, a: `{"a": 2, "b": 1}`, b: `{"a": 2, "b": 1}`,
			want: `{"a":2,"b":1}`},
		{name: "whitespace inside a value is not a change",
			base: `{"a": {"x": 1}, "b": 1}`, a: `{"a": {"x":1}, "b": 2}`, b: `{"a": {"x": 1}, "b": 1}`,
			want: `{"a":{"x":1},"b":2}`},
		{name: "a key added on each side: both kept, new keys sorted after the base's",
			base: `{"m": 1}`, a: `{"m": 1, "z": 1}`, b: `{"m": 1, "c": 1}`,
			want: `{"m":1,"c":1,"z":1}`},
		{name: "a key removed on one side and untouched on the other is removed",
			base: `{"a": 1, "b": 1}`, a: `{"b": 1}`, b: `{"a": 1, "b": 2}`,
			want: `{"b":2}`},
		{name: "a key removed on one side and changed on the other is a conflict",
			base: `{"a": 1, "b": 1}`, a: `{"b": 1}`, b: `{"a": 2, "b": 1}`},
		{name: "the same key added with different values is a conflict",
			base: `{}`, a: `{"a": 1}`, b: `{"a": 2}`},
		{name: "a trailing newline in the base is kept",
			base: "{\n  \"a\": 1,\n  \"b\": 1\n}\n", a: `{"a": 2, "b": 1}`, b: `{"a": 1, "b": 2}`,
			want: "{\n  \"a\": 2,\n  \"b\": 2\n}\n"},
		{name: "tab indentation in the base is kept",
			base: "{\n\t\"a\": 1,\n\t\"b\": 1\n}", a: `{"a": 2, "b": 1}`, b: `{"a": 1, "b": 2}`,
			want: "{\n\t\"a\": 2,\n\t\"b\": 2\n}"},
		{name: "everything removed is an empty object",
			base: `{"a": 1}`, a: `{}`, b: `{"a": 1}`, want: `{}`},
		{name: "strings are not escaped again",
			base: `{"a": "<x>", "b": 1}`, a: `{"a": "<x>", "b": 2}`, b: `{"a": "<x>", "b": 1}`,
			want: `{"a":"<x>","b":2}`},
		{name: "an array is not an object", base: `[1]`, a: `[2]`, b: `[3]`, notJSON: true},
		{name: "invalid JSON on one side", base: `{"a": 1}`, a: `{"a": 2`, b: `{"a": 1}`, notJSON: true},
		{name: "duplicate keys are not merged", base: `{"a": 1}`, a: `{"a": 1, "a": 2}`, b: `{"a": 1}`, notJSON: true},
		{name: "trailing data is not JSON", base: `{"a": 1}`, a: `{"a": 2} x`, b: `{"a": 1}`, notJSON: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, isJSON, ok := JSON3([]byte(tc.base), []byte(tc.a), []byte(tc.b), "")
			if isJSON == tc.notJSON {
				t.Fatalf("isJSON = %v", isJSON)
			}
			if tc.notJSON {
				return
			}
			if ok != (tc.want != "") {
				t.Fatalf("ok = %v, got %q", ok, got)
			}
			if ok && string(got) != tc.want {
				t.Fatalf("got\n%s\nwant\n%s", got, tc.want)
			}
			// Both machines make the same bytes, whichever side is local.
			back, _, okBack := JSON3([]byte(tc.base), []byte(tc.b), []byte(tc.a), "")
			if okBack != ok || string(back) != string(got) {
				t.Fatalf("not symmetric: %q vs %q", got, back)
			}
		})
	}
}

// With a newest-by field, a key changed on both sides takes the whole value
// with the larger field; anything it cannot rank stays a conflict.
func TestJSON3NewestBy(t *testing.T) {
	const base = `{"a": {"t": "a0", "exp": 1}, "b": 1}`
	for _, tc := range []struct {
		name, a, b string
		want       string // "" means a conflict
	}{
		{name: "the larger field wins whole",
			a: `{"a": {"t": "a1", "exp": 5}, "b": 1}`, b: `{"a": {"t": "a2", "exp": 3}, "b": 2}`,
			want: `{"a":{"t":"a1","exp":5},"b":2}`},
		{name: "numbers compare by value, not text",
			a: `{"a": {"t": "a1", "exp": 10}, "b": 1}`, b: `{"a": {"t": "a2", "exp": 9.5}, "b": 1}`,
			want: `{"a":{"t":"a1","exp":10},"b":1}`},
		{name: "a tie is a conflict",
			a: `{"a": {"t": "a1", "exp": 5}, "b": 1}`, b: `{"a": {"t": "a2", "exp": 5}, "b": 1}`},
		{name: "a side without the field is a conflict",
			a: `{"a": {"t": "a1", "exp": 5}, "b": 1}`, b: `{"a": {"t": "a2"}, "b": 1}`},
		{name: "a field that is not a number is a conflict",
			a: `{"a": {"t": "a1", "exp": 5}, "b": 1}`, b: `{"a": {"t": "a2", "exp": "9"}, "b": 1}`},
		{name: "a value that is not an object is a conflict",
			a: `{"a": {"t": "a1", "exp": 5}, "b": 1}`, b: `{"a": 7, "b": 1}`},
		{name: "a removed key is a conflict",
			a: `{"a": {"t": "a1", "exp": 5}, "b": 1}`, b: `{"b": 1}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, isJSON, ok := JSON3([]byte(base), []byte(tc.a), []byte(tc.b), "exp")
			if !isJSON || ok != (tc.want != "") {
				t.Fatalf("isJSON %v ok %v, got %q", isJSON, ok, got)
			}
			if ok && string(got) != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
			back, _, okBack := JSON3([]byte(base), []byte(tc.b), []byte(tc.a), "exp")
			if okBack != ok || string(back) != string(got) {
				t.Fatalf("not symmetric: %q vs %q", got, back)
			}
		})
	}
}

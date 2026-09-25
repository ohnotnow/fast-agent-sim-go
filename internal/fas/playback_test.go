package fas

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSegments(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []segment
	}{
		{"plain prose is all typed", "fix the bug", []segment{{false, "fix the bug"}}},
		{"short inline spans are typed", "run `go test` now",
			[]segment{{false, "run `go test`"}, {false, " now"}}},
		{"long inline spans are pasted",
			"see `internal/fas/playback.go:123 thing` ok",
			[]segment{{false, "see `"}, {true, "internal/fas/playback.go:123 thing"}, {false, "`"}, {false, " ok"}}},
		{"fenced blocks paste the body, type the fence and tag",
			"look:\n```go\nfunc x() {}\n```",
			[]segment{{false, "look:\n```go\n"}, {true, "func x() {}\n"}, {false, "```"}, {false, ""}}},
		{"bare long runs paste, trailing punctuation typed",
			"open https://example.com/a/very/long/path/indeed.",
			[]segment{{false, ""}, {false, "open "}, {true, "https://example.com/a/very/long/path/indeed"}, {false, "."}, {false, ""}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := segments(c.in)
			// empty typed segments are harmless; compare what matters
			if !reflect.DeepEqual(nonEmpty(got), nonEmpty(c.want)) {
				t.Errorf("segments(%q)\n got %+v\nwant %+v", c.in, got, c.want)
			}
		})
	}
}

func nonEmpty(segs []segment) []segment {
	var out []segment
	for _, s := range segs {
		if s.text != "" {
			out = append(out, s)
		}
	}
	return out
}

func TestResolveInside(t *testing.T) {
	dir := t.TempDir()
	for _, bad := range []string{"", "/etc/passwd", "~/x", "../escape", "a/../../escape"} {
		if _, ok := resolveInside(dir, bad); ok {
			t.Errorf("%q should be refused", bad)
		}
	}
	got, ok := resolveInside(dir, "./src/app.py")
	if !ok || got != filepath.Join(dir, "src", "app.py") {
		t.Errorf("got %q, %v", got, ok)
	}
}

func TestMaterialise(t *testing.T) {
	dir := t.TempDir()
	write := Event{Name: "Write", Input: map[string]any{"file_path": "./src/app.py", "content": "a = 1\nb = 1\n"}}
	materialise(write, dir)
	edit := Event{Name: "Edit", Input: map[string]any{"file_path": "./src/app.py", "old_string": "1", "new_string": "2", "replace_all": true}}
	materialise(edit, dir)

	got, err := os.ReadFile(filepath.Join(dir, "src", "app.py"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "a = 2\nb = 2\n" {
		t.Errorf("file = %q", got)
	}

	materialise(Event{Name: "Write", Input: map[string]any{"file_path": "/tmp/outside.txt", "content": "x"}}, dir)
	if _, err := os.Stat(filepath.Join(dir, "tmp", "outside.txt")); err == nil {
		t.Error("absolute path was materialised inside the workdir")
	}
}

func TestLineDiff(t *testing.T) {
	old := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	updated := []string{"a", "b", "c", "d", "E", "f", "g", "h"}
	got := strings.Join(lineDiff(old, updated), "|")
	want := " b| c| d|-e|+E| f| g| h"
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if lineDiff(old, old) != nil {
		t.Error("identical snippets should produce no diff")
	}
}

func TestPacedGap(t *testing.T) {
	prev := 0.0
	if got := pacedGap(50, &prev); got != maxGap {
		t.Errorf("long think = %v, want clamp to %v", got, maxGap)
	}
	if got := pacedGap(0.1, &prev); got != minGap {
		t.Errorf("tiny gap = %v, want floor %v", got, minGap)
	}
	if got := pacedGap(10, &prev); got != 1.0 {
		t.Errorf("10s gap = %v, want 1s", got)
	}
}

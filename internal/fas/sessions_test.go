package fas

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProjectPath(t *testing.T) {
	home, _ := os.UserHomeDir()
	parent := t.TempDir()
	if err := os.Mkdir(filepath.Join(parent, "my_app"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(parent)

	cases := []struct {
		in, want string
		ok       bool
	}{
		{"/some/deleted/project/", "/some/deleted/project", true},
		{"~/code/app", filepath.Join(home, "code", "app"), true},
		{"./my_app", filepath.Join(parent, "my_app"), true},
		{"my_app", filepath.Join(parent, "my_app"), true}, // an existing dir
		{"other_app", "", false},                          // a name fragment
		{"", "", false},
	}
	for _, c := range cases {
		got, ok := projectPath(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("projectPath(%q) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

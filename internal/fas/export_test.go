package fas

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testScript(text string) *Script {
	return &Script{Events: []Event{
		{Type: "meta", Version: 1, Scenario: "session"},
		{Type: "prompt", T: 1, Text: "hello"},
		{Type: "text", T: 2, Text: text},
	}}
}

func TestExportWritesACleanRecording(t *testing.T) {
	t.Chdir(t.TempDir())
	out, err := export(testScript("all done in ./app.py"), "my-demo")
	if err != nil {
		t.Fatal(err)
	}
	s, err := loadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "my-demo" || len(s.Events) != 3 {
		t.Errorf("round trip = %+v", s)
	}

	if _, err := export(testScript("again"), "my-demo"); err == nil {
		t.Error("export should refuse to overwrite an existing file")
	}
}

func TestExportTripWire(t *testing.T) {
	t.Chdir(t.TempDir())
	_, err := export(testScript("see /Users/someone-else/secret.txt"), "leaky")
	if err == nil || !strings.Contains(err.Error(), "TRIP-WIRE") {
		t.Fatalf("want trip-wire refusal, got %v", err)
	}
	if _, err := os.Stat("leaky.jsonl"); err == nil {
		t.Error("a refused export still wrote its file")
	}
}

func TestExportReplacesUsername(t *testing.T) {
	username := currentUsername()
	if len(username) < 3 {
		t.Skip("username too short to test meaningfully")
	}
	t.Chdir(t.TempDir())
	out, err := export(testScript("owned by "+username), "named")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Clean(out))
	if strings.Contains(string(data), username) || !strings.Contains(string(data), "owned by user") {
		t.Errorf("username survived export: %s", data)
	}
}

func TestDemosLoadAndPlay(t *testing.T) {
	demos, err := loadDemos()
	if err != nil {
		t.Fatal(err)
	}
	if len(demos) == 0 {
		t.Fatal("no demos embedded")
	}
	for _, d := range demos {
		if !d.playable() {
			t.Errorf("demo %s has nothing to play", d.Name)
		}
		if d.Tarball != nil {
			dir := t.TempDir()
			if err := extractTarball(d.Tarball, dir); err != nil {
				t.Errorf("demo %s tarball: %v", d.Name, err)
			}
		}
	}
}

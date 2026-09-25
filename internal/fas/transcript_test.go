package fas

import (
	"strings"
	"testing"
)

func TestParseClaudeTranscript(t *testing.T) {
	transcript := strings.Join([]string{
		`{"type":"user","cwd":"/work/proj","timestamp":"2026-01-01T10:00:00Z","message":{"role":"user","content":"<command-name>/clear</command-name>"}}`,
		`{"type":"user","cwd":"/work/proj","timestamp":"2026-01-01T10:00:01Z","message":{"role":"user","content":"Build me a thing please"}}`,
		`{"type":"assistant","timestamp":"2026-01-01T10:00:05Z","message":{"model":"claude-test","content":[{"type":"thinking","thinking":"hmm"},{"type":"text","text":"On it."}]}}`,
		`{"type":"assistant","isSidechain":true,"timestamp":"2026-01-01T10:00:06Z","message":{"content":[{"type":"text","text":"sub-agent chatter"}]}}`,
		`{"type":"assistant","timestamp":"2026-01-01T10:00:10Z","message":{"content":[{"type":"tool_use","id":"t1","name":"Write","input":{"file_path":"/work/proj/app.py","content":"print(1)\n"}}]}}`,
		`{"type":"user","timestamp":"2026-01-01T10:00:11Z","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"File created at /work/proj/app.py"}]}]}}`,
		`not json at all`,
		`{"type":"user","timestamp":"2026-01-01T10:01:00Z","message":{"content":"Thanks!"}}`,
	}, "\n")

	s, err := parseAny([]byte(transcript))
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for _, e := range s.Events {
		types = append(types, e.Type)
	}
	want := "meta prompt thinking text tool_call tool_result turn_end prompt turn_end"
	if got := strings.Join(types, " "); got != want {
		t.Fatalf("event types:\n got %s\nwant %s", got, want)
	}

	t.Run("meta carries the model", func(t *testing.T) {
		if s.Events[0].Model != "claude-test" || s.Kind != "claude" {
			t.Errorf("meta = %+v, kind %q", s.Events[0], s.Kind)
		}
	})
	t.Run("timestamps are rebased onto the first prompt", func(t *testing.T) {
		if s.Events[1].T != 0 || s.Events[4].T != 9 {
			t.Errorf("prompt t=%v, tool_call t=%v", s.Events[1].T, s.Events[4].T)
		}
	})
	t.Run("the session cwd becomes relative so files materialise", func(t *testing.T) {
		if got := str(s.Events[4].Input, "file_path"); got != "./app.py" {
			t.Errorf("file_path = %q", got)
		}
		if got := s.Events[5].Content; got != "File created at ./app.py" {
			t.Errorf("result = %q", got)
		}
	})
	t.Run("turn_end measures the turn", func(t *testing.T) {
		if s.Events[6].DurationMs != 10000 {
			t.Errorf("duration_ms = %d", s.Events[6].DurationMs)
		}
	})
	if s.FirstPrompt() != "Build me a thing please" {
		t.Errorf("first prompt = %q", s.FirstPrompt())
	}
}

func TestParseCodexRollout(t *testing.T) {
	patch := "apply_patch <<'EOF'\n*** Begin Patch\n*** Add File: hello.txt\n+hi\n+there\n*** Update File: old.txt\n@@\n-a\n+b\n*** Delete File: gone.txt\n*** End Patch\nEOF"
	args := `{"command":` + jsonQuote(patch) + `}`
	rollout := strings.Join([]string{
		`{"type":"session_meta","timestamp":"2026-01-01T10:00:00Z","payload":{"cwd":"/work/proj"}}`,
		`{"type":"turn_context","timestamp":"2026-01-01T10:00:00Z","payload":{"model":"gpt-test"}}`,
		`{"type":"event_msg","timestamp":"2026-01-01T10:00:01Z","payload":{"type":"user_message","message":"make hello.txt"}}`,
		`{"type":"response_item","timestamp":"2026-01-01T10:00:02Z","payload":{"type":"reasoning"}}`,
		`{"type":"response_item","timestamp":"2026-01-01T10:00:03Z","payload":{"type":"function_call","name":"shell","call_id":"c1","arguments":` + jsonQuote(args) + `}}`,
		`{"type":"response_item","timestamp":"2026-01-01T10:00:04Z","payload":{"type":"function_call_output","call_id":"c1","output":"Done!"}}`,
		`{"type":"response_item","timestamp":"2026-01-01T10:00:05Z","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Made it."}]}}`,
	}, "\n")

	s, err := parseAny([]byte(rollout))
	if err != nil {
		t.Fatal(err)
	}
	if s.Kind != "codex" || s.Events[0].Model != "gpt-test" {
		t.Fatalf("kind %q, meta %+v", s.Kind, s.Events[0])
	}

	var calls []Event
	for _, e := range s.Events {
		if e.Type == "tool_call" {
			calls = append(calls, e)
		}
	}
	if len(calls) != 3 {
		t.Fatalf("want one apply_patch per file, got %d calls", len(calls))
	}
	if calls[0].Name != "apply_patch" || str(calls[0].Input, "content") != "hi\nthere\n" {
		t.Errorf("add = %+v", calls[0].Input)
	}
	if str(calls[1].Input, "diff") != "@@\n-a\n+b" {
		t.Errorf("update = %+v", calls[1].Input)
	}
	if calls[2].Input["delete"] != true {
		t.Errorf("delete = %+v", calls[2].Input)
	}
}

func TestParseAnyReadsExportedRecordings(t *testing.T) {
	recording := `{"type":"meta","t":0,"version":1,"scenario":"my-demo"}
{"type":"prompt","t":1,"text":"hi"}
{"type":"text","t":2,"text":"hello"}`
	s, err := parseAny([]byte(recording))
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "my-demo" || s.Title != "My Demo" || len(s.Events) != 3 {
		t.Errorf("script = %+v", s)
	}
}

func TestResultTextTruncates(t *testing.T) {
	long := strings.Repeat("é", resultTruncate+10)
	got := resultText(long)
	if !strings.HasSuffix(got, "...[truncated]") || !strings.HasPrefix(got, "éé") {
		t.Errorf("truncated to %q...", got[:10])
	}
}

func jsonQuote(s string) string {
	return `"` + jsonInner(s) + `"`
}

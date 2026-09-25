package fas

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Two transcript dialects are understood, sniffed from the file itself:
//
//   - Claude Code sessions: ~/.claude/projects/<munged-cwd>/<session-id>.jsonl
//   - Codex rollouts: ~/.codex/sessions/<yyyy>/<mm>/<dd>/rollout-*.jsonl
//     (these open with a session_meta line - that's the sniff)
//
// Either becomes the recording schema in memory, with pacing taken from
// the transcript's own timestamps. Codex rollouts carry no schema
// version and their shape drifts between CLI releases, so lines are
// handled by shape and anything unrecognised is skipped, not fatal.

const resultTruncate = 4000

// User lines starting with these aren't prompts a person typed: command
// wrappers (<command-name>...), interruption notices, hook caveats.
var skipPromptPrefixes = []string{"<", "[Request interrupted", "Caveat:"}

// codex apply_patch envelope markers, one section per touched file.
const (
	patchBegin  = "*** Begin Patch"
	patchEnd    = "*** End Patch"
	patchAdd    = "*** Add File: "
	patchUpdate = "*** Update File: "
	patchDelete = "*** Delete File: "
)

type row struct {
	when  float64 // epoch seconds
	event Event
}

// loadFile reads a Claude Code transcript, a Codex rollout or an
// exported recording, whichever it turns out to be.
func loadFile(path string) (*Script, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	s, err := parseAny(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	s.Path = path
	s.When = info.ModTime()
	if s.Title == "" {
		s.Title = s.FirstPrompt()
	}
	return s, nil
}

func parseAny(data []byte) (*Script, error) {
	var lines []map[string]any
	scanner := newLineScanner(data)
	for scanner.Scan() {
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			continue
		}
		var line map[string]any
		if json.Unmarshal(raw, &line) != nil {
			continue // a torn line mid-write shouldn't sink the session
		}
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(lines) == 0 {
		return nil, fmt.Errorf("no events found")
	}

	switch str(lines[0], "type") {
	case "meta":
		events, err := parseRecording(data)
		if err != nil {
			return nil, err
		}
		name := events[0].Scenario
		return &Script{Name: name, Title: displayName(name), Kind: "recording", Events: events}, nil
	case "session_meta":
		model, cwd, rows := parseCodex(lines)
		return transcriptScript("codex", model, cwd, rows)
	default:
		model, cwd, rows := parseClaude(lines)
		return transcriptScript("claude", model, cwd, rows)
	}
}

func transcriptScript(kind, model, cwd string, rows []row) (*Script, error) {
	if len(rows) == 0 {
		return nil, fmt.Errorf("no convertible events found - is this a supported session transcript?")
	}
	source := map[string]string{"claude": "claude-code-transcript", "codex": "codex-rollout"}[kind]
	events := assemble(rows, "session", model, source)
	events = cleanPaths(events, cwd)
	return &Script{Name: "session", Kind: kind, Events: events}, nil
}

// assemble rebases row timestamps onto t=0 and inserts turn boundaries.
func assemble(rows []row, scenario, model, source string) []Event {
	first := rows[0].when
	events := []Event{{
		Type: "meta", Version: 1, Scenario: scenario, Model: model, Source: source,
		RecordedAt: time.Unix(0, int64(first*1e9)).Format(time.RFC3339),
	}}
	lastT := 0.0
	var promptT *float64

	endTurn := func() {
		if promptT != nil {
			events = append(events, Event{Type: "turn_end", T: lastT,
				DurationMs: int64((lastT - *promptT) * 1000)})
			promptT = nil
		}
	}

	for _, r := range rows {
		if r.event.Type == "prompt" {
			endTurn()
		}
		lastT = math.Max(math.Round((r.when-first)*1000)/1000, lastT)
		e := r.event
		e.T = lastT
		events = append(events, e)
		if e.Type == "prompt" {
			t := lastT
			promptT = &t
		}
	}
	endTurn()
	return events
}

// --- Claude Code transcripts ------------------------------------------------

func parseClaude(lines []map[string]any) (model, cwd string, rows []row) {
	cwd = "."
	foundCwd := false
	for _, line := range lines {
		kind := str(line, "type")
		if kind != "user" && kind != "assistant" {
			continue
		}
		if b, _ := line["isSidechain"].(bool); b {
			continue
		}
		if b, _ := line["isMeta"].(bool); b {
			continue
		}
		message, ok := line["message"].(map[string]any)
		if !ok {
			continue
		}
		if !foundCwd && str(line, "cwd") != "" {
			cwd, foundCwd = str(line, "cwd"), true
		}
		if kind == "assistant" && model == "" {
			model = str(message, "model")
		}
		when, ok := epoch(line)
		if !ok {
			continue
		}
		content := message["content"]
		blocks, _ := content.([]any)

		if kind == "assistant" {
			for _, b := range blocks {
				block, ok := b.(map[string]any)
				if !ok {
					continue
				}
				switch str(block, "type") {
				case "text":
					if strings.TrimSpace(str(block, "text")) != "" {
						rows = append(rows, row{when, Event{Type: "text", Text: str(block, "text")}})
					}
				case "thinking":
					rows = append(rows, row{when, Event{Type: "thinking"}})
				case "tool_use":
					input, _ := block["input"].(map[string]any)
					rows = append(rows, row{when, Event{Type: "tool_call",
						ID: str(block, "id"), Name: str(block, "name"), Input: input}})
				}
			}
			continue
		}

		if hasToolResult(blocks) {
			for _, b := range blocks {
				block, ok := b.(map[string]any)
				if !ok || str(block, "type") != "tool_result" {
					continue
				}
				isErr, _ := block["is_error"].(bool)
				rows = append(rows, row{when, Event{Type: "tool_result",
					ToolUseID: str(block, "tool_use_id"), IsError: isErr,
					Content: resultText(block["content"])}})
			}
		} else if text, ok := promptText(content); ok {
			rows = append(rows, row{when, Event{Type: "prompt", Text: text}})
		}
	}
	return model, cwd, rows
}

func hasToolResult(blocks []any) bool {
	for _, b := range blocks {
		if block, ok := b.(map[string]any); ok && str(block, "type") == "tool_result" {
			return true
		}
	}
	return false
}

// promptText is the prompt a person actually typed, if this is one.
func promptText(content any) (string, bool) {
	var text string
	switch c := content.(type) {
	case string:
		text = c
	case []any:
		var parts []string
		for _, b := range c {
			if block, ok := b.(map[string]any); ok && str(block, "type") == "text" {
				parts = append(parts, str(block, "text"))
			}
		}
		text = strings.Join(parts, "\n")
	default:
		return "", false
	}
	text = strings.TrimSpace(text)
	if text == "" || hasSkipPrefix(text) {
		return "", false
	}
	return text, true
}

func hasSkipPrefix(text string) bool {
	for _, prefix := range skipPromptPrefixes {
		if strings.HasPrefix(text, prefix) {
			return true
		}
	}
	return false
}

// --- Codex rollouts ---------------------------------------------------------

func parseCodex(lines []map[string]any) (model, cwd string, rows []row) {
	cwd = "."
	if payload, ok := lines[0]["payload"].(map[string]any); ok && str(payload, "cwd") != "" {
		cwd = str(payload, "cwd")
	}
	for _, line := range lines {
		payload, ok := line["payload"].(map[string]any)
		if !ok {
			continue
		}
		lineType, payloadType := str(line, "type"), str(payload, "type")
		if lineType == "turn_context" && model == "" {
			model = str(payload, "model")
		}
		when, ok := epoch(line)
		if !ok {
			continue
		}

		switch {
		case lineType == "event_msg" && payloadType == "user_message":
			text := str(payload, "message")
			if text == "" {
				text = str(payload, "text")
			}
			text = strings.TrimSpace(text)
			if text != "" && !hasSkipPrefix(text) {
				rows = append(rows, row{when, Event{Type: "prompt", Text: text}})
			}
		case lineType == "event_msg" && payloadType == "patch_apply_end":
			if ok, _ := payload["success"].(bool); ok {
				changes, _ := payload["changes"].(map[string]any)
				for _, input := range patchEndFiles(changes) {
					rows = append(rows, row{when, Event{Type: "tool_call",
						ID: str(payload, "call_id"), Name: "apply_patch", Input: input}})
				}
			}
		case lineType != "response_item":
		case payloadType == "message":
			if str(payload, "role") != "assistant" {
				continue
			}
			blocks, _ := payload["content"].([]any)
			for _, b := range blocks {
				if block, ok := b.(map[string]any); ok && strings.TrimSpace(str(block, "text")) != "" {
					rows = append(rows, row{when, Event{Type: "text", Text: str(block, "text")}})
				}
			}
		case payloadType == "reasoning":
			rows = append(rows, row{when, Event{Type: "thinking"}})
		case payloadType == "function_call" || payloadType == "custom_tool_call":
			for _, e := range codexCall(payload) {
				rows = append(rows, row{when, e})
			}
		case payloadType == "function_call_output" || payloadType == "custom_tool_call_output":
			rows = append(rows, row{when, Event{Type: "tool_result",
				ToolUseID: str(payload, "call_id"), Content: codexResult(payload["output"])}})
		case payloadType == "web_search_call":
			action, _ := payload["action"].(map[string]any)
			rows = append(rows, row{when, Event{Type: "tool_call", ID: str(payload, "id"),
				Name: "web_search", Input: map[string]any{"query": str(action, "query")}}})
		}
	}
	return model, cwd, rows
}

// codexCall is the tool_call event(s) for a function_call or
// custom_tool_call. An exec whose command is an apply_patch heredoc
// becomes apply_patch events instead - one telling of the act, not two.
func codexCall(payload map[string]any) []Event {
	callID := str(payload, "call_id")
	if callID == "" {
		callID = str(payload, "id")
	}
	var args map[string]any
	if str(payload, "type") == "function_call" {
		raw := str(payload, "arguments")
		if raw == "" {
			raw = "{}"
		}
		if json.Unmarshal([]byte(raw), &args) != nil || args == nil {
			args = map[string]any{"arguments": payload["arguments"]}
		}
	} else {
		// custom_tool_call input is one raw string; keep it honest and
		// let playback truncate.
		args = map[string]any{"command": str(payload, "input")}
	}

	cmd := str(args, "cmd")
	if cmd == "" {
		cmd = str(args, "command")
	}
	if patched := applyPatchFiles(cmd); patched != nil {
		events := make([]Event, 0, len(patched))
		for _, input := range patched {
			events = append(events, Event{Type: "tool_call", ID: callID, Name: "apply_patch", Input: input})
		}
		return events
	}
	name := str(payload, "name")
	if name == "" {
		name = "tool"
	}
	return []Event{{Type: "tool_call", ID: callID, Name: name, Input: args}}
}

// applyPatchFiles splits an apply_patch envelope into per-file inputs,
// or returns nil when cmd isn't an apply_patch invocation. Added files
// carry full content, updates carry the hunk text as a display diff,
// deletes carry a flag.
func applyPatchFiles(cmd string) []map[string]any {
	lines := strings.Split(cmd, "\n")
	begin, end := indexOf(lines, patchBegin), indexOf(lines, patchEnd)
	if begin < 0 || end < 0 || !strings.Contains(strings.Join(lines[:begin], "\n"), "apply_patch") {
		return nil
	}

	var files []map[string]any
	kind, path := "", ""
	var body []string

	flush := func() {
		switch kind {
		case "add":
			var added []string
			for _, l := range body {
				if strings.HasPrefix(l, "+") {
					added = append(added, l[1:])
				}
			}
			content := ""
			if len(added) > 0 {
				content = strings.Join(added, "\n") + "\n"
			}
			files = append(files, map[string]any{"file_path": path, "content": content})
		case "update":
			files = append(files, map[string]any{"file_path": path, "diff": strings.Join(body, "\n")})
		}
	}

	for _, line := range lines[begin+1 : max(end, begin+1)] {
		switch {
		case strings.HasPrefix(line, patchAdd):
			flush()
			kind, path, body = "add", strings.TrimSpace(line[len(patchAdd):]), nil
		case strings.HasPrefix(line, patchUpdate):
			flush()
			kind, path, body = "update", strings.TrimSpace(line[len(patchUpdate):]), nil
		case strings.HasPrefix(line, patchDelete):
			flush()
			kind, path, body = "", "", nil
			files = append(files, map[string]any{
				"file_path": strings.TrimSpace(line[len(patchDelete):]), "delete": true})
		default:
			body = append(body, line)
		}
	}
	flush()
	if len(files) == 0 {
		return nil
	}
	return files
}

// patchEndFiles is per-file apply_patch inputs from a patch_apply_end
// event's changes.
func patchEndFiles(changes map[string]any) []map[string]any {
	var files []map[string]any
	for path, c := range changes {
		change, ok := c.(map[string]any)
		if !ok {
			continue
		}
		entry := map[string]any{"file_path": path}
		content := str(change, "content")
		if content == "" {
			content = str(change, "new_content")
		}
		diff := str(change, "unified_diff")
		if diff == "" {
			diff = str(change, "diff")
		}
		switch {
		case str(change, "type") == "delete" || str(change, "type") == "remove":
			entry["delete"] = true
		case content != "":
			entry["content"] = content
		case diff != "":
			entry["diff"] = diff
		}
		files = append(files, entry)
	}
	return files
}

// codexResult: outputs are plain text (older CLIs) or a JSON-encoded
// block list.
func codexResult(output any) string {
	if s, ok := output.(string); ok {
		var parsed []any
		if json.Unmarshal([]byte(s), &parsed) == nil {
			return resultText(parsed)
		}
	}
	return resultText(output)
}

// --- Shared helpers ---------------------------------------------------------

// resultText normalises tool result content (string, block list, nil).
func resultText(content any) string {
	var text string
	switch c := content.(type) {
	case nil:
		return ""
	case string:
		text = c
	case []any:
		parts := make([]string, 0, len(c))
		for _, p := range c {
			if part, ok := p.(map[string]any); ok {
				parts = append(parts, str(part, "text"))
			}
		}
		text = strings.Join(parts, "\n")
	default:
		text = fmt.Sprint(c)
	}
	if runes := []rune(text); len(runes) > resultTruncate {
		text = string(runes[:resultTruncate]) + "\n...[truncated]"
	}
	return text
}

// cleanPaths rewrites the session's working directory to "." and $HOME
// to "~". Beyond tidiness this is load-bearing: playback only
// materialises files at relative paths, so an absolute Write path from
// a real session has to become "./thing" first.
func cleanPaths(events []Event, cwd string) []Event {
	var replacements []string
	if cwd != "" && cwd != "." && cwd != "/" {
		variants := []string{cwd}
		if resolved, err := filepath.EvalSymlinks(cwd); err == nil && resolved != cwd {
			variants = append(variants, resolved)
		}
		if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
			variants = append(variants, "/private"+resolved)
		}
		for _, v := range variants {
			replacements = append(replacements, v, ".")
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" && home != "/" {
		replacements = append(replacements, home, "~")
	}
	return rewriteEvents(events, replacements)
}

// rewriteEvents applies old/new string pairs across every field of every
// event, by rewriting the JSON form. Needles are JSON-encoded too, so a
// Windows path's backslashes match their escaped form.
func rewriteEvents(events []Event, pairs []string) []Event {
	if len(pairs) == 0 {
		return events
	}
	encoded := make([]string, len(pairs))
	for i, p := range pairs {
		encoded[i] = jsonInner(p)
	}
	replacer := strings.NewReplacer(encoded...)
	out := make([]Event, 0, len(events))
	for _, e := range events {
		raw, err := marshalEvent(e)
		if err != nil {
			out = append(out, e)
			continue
		}
		var cleaned Event
		if json.Unmarshal([]byte(replacer.Replace(string(raw))), &cleaned) != nil {
			out = append(out, e)
			continue
		}
		out = append(out, cleaned)
	}
	return out
}

func marshalEvent(e Event) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(e); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func jsonInner(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	out := strings.TrimSpace(buf.String())
	return out[1 : len(out)-1]
}

func epoch(line map[string]any) (float64, bool) {
	ts := str(line, "timestamp")
	if ts == "" {
		return 0, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return 0, false
	}
	return float64(parsed.UnixNano()) / 1e9, true
}

func str(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	s, _ := m[key].(string)
	return s
}

func indexOf(lines []string, want string) int {
	for i, l := range lines {
		if l == want {
			return i
		}
	}
	return -1
}

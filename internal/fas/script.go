package fas

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"
)

// Event is one line of a recording: the schema the original Python
// record.py wrote, and what transcripts are converted into in memory.
type Event struct {
	Type       string         `json:"type"`
	T          float64        `json:"t"`
	Version    int            `json:"version,omitempty"`
	Scenario   string         `json:"scenario,omitempty"`
	Model      string         `json:"model,omitempty"`
	Source     string         `json:"source,omitempty"`
	RecordedAt string         `json:"recorded_at,omitempty"`
	Text       string         `json:"text,omitempty"`
	ID         string         `json:"id,omitempty"`
	Name       string         `json:"name,omitempty"`
	Input      map[string]any `json:"input,omitempty"`
	ToolUseID  string         `json:"tool_use_id,omitempty"`
	IsError    bool           `json:"is_error,omitempty"`
	Content    string         `json:"content,omitempty"`
	DurationMs int64          `json:"duration_ms,omitempty"`
}

// Script is anything playable: a shipped demo or a real session.
type Script struct {
	Name    string    // short name, used for the temp project dir
	Title   string    // what the picker shows
	Kind    string    // "demo", "claude" or "codex"
	Path    string    // source file, for sessions
	When    time.Time // last activity, for sessions
	Events  []Event
	Tarball []byte // final project files, unpacked after playback (demos only)
}

// FirstPrompt is the opening prompt flattened onto one line.
func (s *Script) FirstPrompt() string {
	for _, e := range s.Events {
		if e.Type == "prompt" {
			return strings.Join(strings.Fields(e.Text), " ")
		}
	}
	return ""
}

// searchText is what the picker filter matches against: every prompt,
// since the prompts are what people remember a session by.
func (s *Script) searchText() string {
	var b strings.Builder
	b.WriteString(strings.ToLower(s.Title))
	for _, e := range s.Events {
		if e.Type == "prompt" {
			b.WriteString("\n")
			b.WriteString(strings.ToLower(e.Text))
		}
	}
	return b.String()
}

// TimingTag is "(orig: 40m, sim: 3m)" when the compression tells a
// story, or just "(est 2m)" when the original was no slower.
func (s *Script) TimingTag() string {
	if len(s.Events) == 0 {
		return ""
	}
	orig, sim := s.Events[len(s.Events)-1].T, estimatePlaytime(s.Events)
	if orig > sim {
		return fmt.Sprintf("(orig: %s, sim: %s)", fmtDuration(orig), fmtDuration(sim))
	}
	return fmt.Sprintf("(est %s)", fmtDuration(sim))
}

// playable is true when there is something to watch: a prompt and at
// least one thing the agent did about it.
func (s *Script) playable() bool {
	prompts, others := 0, 0
	for _, e := range s.Events {
		switch e.Type {
		case "prompt":
			prompts++
		case "meta", "turn_end":
		default:
			others++
		}
	}
	return prompts > 0 && others > 0
}

func parseRecording(data []byte) ([]Event, error) {
	var events []Event
	scanner := newLineScanner(data)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var e Event
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, fmt.Errorf("bad recording line: %w", err)
		}
		events = append(events, e)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, fmt.Errorf("empty recording")
	}
	return events, nil
}

// newLineScanner copes with transcript lines far longer than bufio's
// default 64KB (a Write of a big file is one line).
func newLineScanner(data []byte) *bufio.Scanner {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 1024*1024), 256*1024*1024)
	return scanner
}

func pacedGap(t float64, prev *float64) float64 {
	if prev == nil {
		return minGap
	}
	return math.Min(math.Max((t-*prev)/speed, minGap), maxGap)
}

// estimatePlaytime is a ballpark of seconds to watch, from the same
// constants the player uses.
func estimatePlaytime(events []Event) float64 {
	keystroke := keystrokeSeconds()
	stroke := keystroke * (typeJitterLow + typeJitterHigh) / 2
	total := 0.0
	var prev *float64
	for _, e := range events {
		if e.Type == "meta" {
			continue
		}
		if e.Type == "prompt" {
			total += readingPause
			for _, seg := range segments(e.Text) {
				if seg.paste {
					total += 2 * pastePause
					continue
				}
				total += float64(utf8.RuneCountInString(seg.text)) * stroke
				for _, r := range seg.text {
					if strings.ContainsRune(pauseKeys, r) {
						total += keystroke * 3
					}
				}
			}
		} else {
			total += pacedGap(e.T, prev)
			if e.Type == "text" {
				total += float64(strings.Count(e.Text, "\n")+1) * lineInterval
			}
		}
		t := e.T
		prev = &t
	}
	return total
}

func fmtDuration(seconds float64) string {
	if seconds < 90 {
		return fmt.Sprintf("%ds", max(int(math.Round(seconds)), 1))
	}
	return fmt.Sprintf("%dm", int(math.Round(seconds/60)))
}

package fas

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
)

// export writes a session as a demo recording, for shipping in demos/.
// This is the one path where a session leaves your machine, so it is
// the one path with the exposure trip-wire: after rewriting paths and
// the username, it refuses to save anything that still smells of the
// local machine.
func export(s *Script, name string) (string, error) {
	out := name + ".jsonl"
	if _, err := os.Stat(out); err == nil {
		return "", fmt.Errorf("%s already exists - pick another name", out)
	}

	events := append([]Event(nil), s.Events...)
	if len(events) > 0 && events[0].Type == "meta" {
		events[0].Scenario = name
	}
	username := currentUsername()
	if username != "" {
		// A bare username still leaks with no path around it: ls -l owner
		// columns, git log authors. The trip-wire stays as the backstop.
		events = rewriteEvents(events, []string{username, "user"})
	}

	var b strings.Builder
	for _, e := range events {
		raw, err := marshalEvent(e)
		if err != nil {
			return "", err
		}
		b.Write(raw)
		b.WriteString("\n")
	}
	if found := exposure(b.String(), username); found != "" {
		return "", fmt.Errorf("EXPOSURE TRIP-WIRE: the recording still contains a machine-specific string (%s) "+
			"after path rewriting. Not saving - pick a different session, or tidy that one up first", found)
	}
	if err := os.WriteFile(out, []byte(b.String()), 0o644); err != nil {
		return "", err
	}
	return out, nil
}

// exposure names the first machine-specific string still present, or "".
func exposure(text, username string) string {
	home, _ := os.UserHomeDir()
	checks := []struct{ needle, label string }{
		{username, "your username"},
		{jsonInner(home), "your home path"},
		{"/Users/", "a /Users/ path"},
		{"/home/", "a /home/ path"},
		{jsonInner(`C:\Users\`), `a C:\Users\ path`},
	}
	for _, c := range checks {
		if c.needle != "" && strings.Contains(text, c.needle) {
			return c.label
		}
	}
	return ""
}

func currentUsername() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		// Windows reports DOMAIN\name.
		return filepath.Base(strings.ReplaceAll(u.Username, `\`, "/"))
	}
	return os.Getenv("USER")
}

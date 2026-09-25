package fas

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
)

// Claude Code stores transcripts under ~/.claude/projects/, in a
// directory named after the session's working directory with every
// non-alphanumeric character flattened to "-" (/Users/you/code/my_app
// becomes -Users-you-code-my-app). Codex files rollouts by date under
// ~/.codex/sessions/ instead, so those are matched on the working
// directory recorded in each file's first line.

var nonAlnum = regexp.MustCompile(`[^A-Za-z0-9]`)

func munge(s string) string {
	return nonAlnum.ReplaceAllString(s, "-")
}

// findSessions gathers every playable Claude Code and Codex session for
// a project (the current directory when project is empty), newest first.
func findSessions(project string) ([]*Script, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	if dir, ok := projectPath(project); ok {
		cwd, project = dir, "" // exactly as if run from inside it
	}

	var paths []string
	claudeDir, err := claudeProjectDir(filepath.Join(home, ".claude", "projects"), project, cwd)
	if err != nil {
		return nil, err
	}
	if claudeDir != "" {
		matches, _ := filepath.Glob(filepath.Join(claudeDir, "*.jsonl"))
		current := os.Getenv("CLAUDE_CODE_SESSION_ID")
		for _, m := range matches {
			// the session you're sitting in is never "that one from last week"
			if current != "" && strings.TrimSuffix(filepath.Base(m), ".jsonl") == current {
				continue
			}
			paths = append(paths, m)
		}
	}
	codex, err := codexRollouts(filepath.Join(home, ".codex", "sessions"), project, cwd)
	if err != nil {
		return nil, err
	}
	paths = append(paths, codex...)

	return loadAll(paths), nil
}

// projectPath resolves a -project value that is a path rather than a
// name fragment. Anything starting /, ~ or . is a path even if it no
// longer exists (a deleted project's sessions are still on disk); a bare
// name only counts when it is a directory that exists.
func projectPath(project string) (string, bool) {
	if project == "" {
		return "", false
	}
	if project == "~" || strings.HasPrefix(project, "~/") || strings.HasPrefix(project, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", false
		}
		project = filepath.Join(home, project[1:])
	} else if !filepath.IsAbs(project) && !strings.HasPrefix(project, ".") {
		if info, err := os.Stat(project); err != nil || !info.IsDir() {
			return "", false
		}
	}
	abs, err := filepath.Abs(project)
	if err != nil {
		return "", false
	}
	return abs, true
}

// claudeProjectDir is the transcript directory for a project, or "" when
// there isn't one. A --project fragment must match exactly one directory.
func claudeProjectDir(root, project, cwd string) (string, error) {
	if project == "" {
		dir := filepath.Join(root, munge(cwd))
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir, nil
		}
		return "", nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", nil
	}
	suffix := "-" + munge(project)
	var matches []string
	for _, e := range entries {
		if e.IsDir() && strings.HasSuffix(e.Name(), suffix) {
			matches = append(matches, e.Name())
		}
	}
	switch len(matches) {
	case 0:
		return "", nil
	case 1:
		return filepath.Join(root, matches[0]), nil
	}
	sort.Strings(matches)
	return "", fmt.Errorf("'%s' is ambiguous - it matches:\n  %s\nGive a longer fragment, e.g. -project code/%s",
		project, strings.Join(matches, "\n  "), project)
}

// codexRollouts lists rollout files recorded in the project's directory.
func codexRollouts(root, project, cwd string) ([]string, error) {
	byCwd := map[string][]string{}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := d.Name()
		if !strings.HasPrefix(name, "rollout-") || !strings.HasSuffix(name, ".jsonl") {
			return nil
		}
		recorded := codexCwd(path)
		if recorded == "" {
			return nil
		}
		if project == "" && recorded != cwd {
			return nil
		}
		if project != "" && !strings.Contains(munge(recorded), munge(project)) {
			return nil
		}
		byCwd[recorded] = append(byCwd[recorded], path)
		return nil
	})
	if len(byCwd) > 1 {
		names := make([]string, 0, len(byCwd))
		for name := range byCwd {
			names = append(names, name)
		}
		sort.Strings(names)
		return nil, fmt.Errorf("'%s' is ambiguous - it matches codex sessions from:\n  %s\nGive a longer fragment.",
			project, strings.Join(names, "\n  "))
	}
	for _, paths := range byCwd {
		return paths, nil
	}
	return nil, nil
}

// codexCwd is a rollout's recorded working directory, from its first
// line only, so filtering stays cheap.
func codexCwd(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	reader := bufio.NewReader(f)
	first, err := reader.ReadBytes('\n')
	if err != nil && len(first) == 0 {
		return ""
	}
	var meta struct {
		Type    string `json:"type"`
		Payload struct {
			Cwd string `json:"cwd"`
		} `json:"payload"`
	}
	if json.Unmarshal(first, &meta) != nil || meta.Type != "session_meta" {
		return ""
	}
	return meta.Payload.Cwd
}

// loadAll parses sessions concurrently, keeping the playable ones,
// newest first. A file that won't parse is skipped rather than fatal:
// one odd transcript shouldn't hide the rest.
func loadAll(paths []string) []*Script {
	results := make([]*Script, len(paths))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range runtime.NumCPU() {
		wg.Go(func() {
			for i := range jobs {
				if s, err := loadFile(paths[i]); err == nil && s.playable() {
					results[i] = s
				}
			}
		})
	}
	for i := range paths {
		jobs <- i
	}
	close(jobs)
	wg.Wait()

	var scripts []*Script
	for _, s := range results {
		if s != nil {
			scripts = append(scripts, s)
		}
	}
	sort.Slice(scripts, func(i, j int) bool { return scripts[i].When.After(scripts[j].When) })
	return scripts
}

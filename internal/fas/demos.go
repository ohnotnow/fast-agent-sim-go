package fas

import (
	"embed"
	"path"
	"sort"
	"strings"
)

// The shipped demos: recordings in the same schema `fas export` writes,
// plus an optional <name>.tar.gz of the final project files for demos
// whose artefacts were born from shell commands (Haido's saved game).
//
//go:embed demos
var demoFS embed.FS

func loadDemos() ([]*Script, error) {
	entries, err := demoFS.ReadDir("demos")
	if err != nil {
		return nil, err
	}
	var demos []*Script
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".jsonl")
		data, err := demoFS.ReadFile(path.Join("demos", e.Name()))
		if err != nil {
			return nil, err
		}
		events, err := parseRecording(data)
		if err != nil {
			return nil, err
		}
		tarball, _ := demoFS.ReadFile(path.Join("demos", name+".tar.gz"))
		demos = append(demos, &Script{
			Name: name, Title: displayName(name), Kind: "demo",
			Events: events, Tarball: tarball,
		})
	}
	sort.Slice(demos, func(i, j int) bool { return demos[i].Name < demos[j].Name })
	return demos, nil
}

// displayName turns "laravel-projects" into "Laravel Projects".
func displayName(name string) string {
	words := strings.Fields(strings.ReplaceAll(name, "-", " "))
	for i, w := range words {
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}

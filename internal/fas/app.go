package fas

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

var (
	Version = "dev" // overwritten by -ldflags at release
	RepoURL = "https://github.com/ohnotnow/fast-agent-sim-go"
)

const usage = `fas - watch a coding agent session replayed at ten times the speed

Usage:
  fas [-project NAME]            pick one of this project's sessions (or a demo) and watch it
  fas play [-auto] FILE          play a session transcript or an exported recording
  fas demo [-auto] [-loop] [NAME]
                                 play the built-in demos (all of them, in order, without NAME)
  fas export [-project NAME] NAME [FILE]
                                 save a session as a demo recording, NAME.jsonl
  fas version                    print the version

Flags:
  -project P      look at another project's sessions: a path (~/code/my_app, /srv/app)
                  or a name fragment (my_app, code/my_app)
  -auto           send each prompt without waiting for Return
  -loop           keep cycling through the demos until Ctrl-C

Sessions come from Claude Code (~/.claude/projects) and Codex (~/.codex/sessions).
While a session plays, Return sends each prompt; type q then Return, or press Ctrl-C, to stop.
`

// Run is the whole CLI; it returns the process exit code.
func Run(args []string) int {
	cmd, rest := "", args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, rest = args[0], args[1:]
	}
	var err error
	switch cmd {
	case "":
		err = runPicker(rest)
	case "play":
		err = runPlay(rest)
	case "demo", "demos":
		err = runDemo(rest)
	case "export":
		err = runExport(rest)
	case "version", "--version":
		fmt.Println("fas", Version)
		return 0
	case "help", "-h", "--help":
		fmt.Print(usage)
		return 0
	default:
		err = fmt.Errorf("unknown command %q", cmd)
	}
	if errors.Is(err, flag.ErrHelp) {
		fmt.Print(usage)
		return 0
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "fas:", err)
		if strings.HasPrefix(err.Error(), "unknown command") || strings.HasPrefix(err.Error(), "usage") {
			fmt.Fprint(os.Stderr, "\n"+usage)
			return 64
		}
		return 1
	}
	return 0
}

func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func runPicker(args []string) error {
	fs := newFlags("fas")
	project := fs.String("project", "", "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	sessions, err := findSessions(*project)
	if err != nil {
		return err
	}
	demos, err := loadDemos()
	if err != nil {
		return err
	}

	title := fmt.Sprintf("fast-agent-sim · %d sessions from %s, plus the demos", len(sessions), projectLabel(*project))
	if len(sessions) == 0 {
		title = fmt.Sprintf("fast-agent-sim · no sessions found for %s, so here are the demos", projectLabel(*project))
	}
	scripts := append(sessions, demos...)

	cursor := 0
	for {
		chosen, pos, err := pick(title, scripts, cursor)
		if err != nil || chosen == nil {
			return err
		}
		cursor = pos
		err = play(chosen, false, true)
		if errors.Is(err, errInterrupted) {
			return nil
		}
		if err != nil && !errors.Is(err, errQuit) {
			return err
		}
		// Hold the stage: the picker's alt screen would otherwise wipe
		// the closing prose before anyone has read it.
		fmt.Print("\n" + styleDim.Render("Return for the session list, q to quit "))
		reply, err := readLine(context.Background())
		if err != nil || isQuit(reply) {
			return nil
		}
	}
}

func projectLabel(project string) string {
	dir, isPath := projectPath(project)
	if project != "" && !isPath {
		return project
	}
	if !isPath {
		var err error
		if dir, err = os.Getwd(); err != nil {
			return "this directory"
		}
	}
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(dir, home) {
		return "~" + strings.TrimPrefix(dir, home)
	}
	return dir
}

func runPlay(args []string) error {
	fs := newFlags("play")
	auto := fs.Bool("auto", false, "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: fas play [-auto] FILE")
	}
	s, err := loadFile(fs.Arg(0))
	if err != nil {
		return err
	}
	return ignoreStop(play(s, *auto, true))
}

func runDemo(args []string) error {
	fs := newFlags("demo")
	auto := fs.Bool("auto", false, "")
	loop := fs.Bool("loop", false, "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	demos, err := loadDemos()
	if err != nil {
		return err
	}
	if name := fs.Arg(0); name != "" {
		demos, err = findDemo(demos, name)
		if err != nil {
			return err
		}
	}

	for {
		for i, d := range demos {
			if i > 0 || *loop {
				fmt.Println(styleDim.Render(strings.Repeat("─", 40)))
				fmt.Println()
			}
			// Nobody pokes the files of a demo on a loop, so don't
			// leave a temp dir behind for every lap.
			if err := play(d, *auto, !*loop); err != nil {
				return ignoreStop(err)
			}
			if *auto {
				time.Sleep(3 * time.Second)
			}
		}
		if !*loop {
			return nil
		}
	}
}

func findDemo(demos []*Script, name string) ([]*Script, error) {
	var names []string
	for _, d := range demos {
		if d.Name == name {
			return []*Script{d}, nil
		}
		names = append(names, d.Name)
	}
	return nil, fmt.Errorf("no demo called %q - try one of: %s", name, strings.Join(names, ", "))
}

func runExport(args []string) error {
	fs := newFlags("export")
	project := fs.String("project", "", "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	name := fs.Arg(0)
	if name == "" || fs.NArg() > 2 || strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("usage: fas export [-project NAME] NAME [FILE]")
	}

	var s *Script
	var err error
	if file := fs.Arg(1); file != "" {
		s, err = loadFile(file)
	} else {
		var sessions []*Script
		sessions, err = findSessions(*project)
		if err == nil && len(sessions) == 0 {
			err = fmt.Errorf("no sessions found for %s", projectLabel(*project))
		}
		if err == nil {
			s, _, err = pick("fas export · which session becomes "+name+".jsonl?", sessions, 0)
		}
	}
	if err != nil || s == nil {
		return err
	}

	out, err := export(s, name)
	if err != nil {
		return err
	}
	fmt.Printf("Saved %s. Check it with: fas play %s\n", out, out)
	fmt.Println("To ship it, copy it into internal/fas/demos/ and rebuild.")
	fmt.Println("It contains whatever the session contained - read it before you share it.")
	return nil
}

// ignoreStop treats the operator stopping playback as a normal exit.
func ignoreStop(err error) error {
	if errors.Is(err, errQuit) || errors.Is(err, errInterrupted) {
		return nil
	}
	return err
}

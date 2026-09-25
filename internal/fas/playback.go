package fas

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/muesli/cancelreader"
)

// Playback compresses the recorded pauses speed-fold, with long thinks
// clamped to maxGap so a 50-second ponder lands as a short dramatic
// beat. Each recorded prompt is typed out at the ❯ as if the operator
// wrote it, and Return sends it (or, with auto, the player does).
//
// The point of honesty: Write/Edit calls (and codex apply_patch
// additions) materialise real files into a temp project dir as they
// happen, and a demo's tarball is unpacked over it at the end. The
// audience can go and poke the real files afterwards.
const (
	speed          = 10.0     // recorded seconds per played second
	minGap         = 0.08     // floor keeps instant-but-sequential legible
	maxGap         = 2.0      // a 50s think becomes a 2s beat
	spinnerFrom    = 0.5      // gaps this long get the spinner, not dead air
	typingWPM      = 110      // the human hasn't sped up: brisk but real typing
	typeJitterLow  = 0.5      // per-keystroke spread, so it isn't metronomic
	typeJitterHigh = 1.7      //
	pauseKeys      = ".,;:!?" // keystrokes followed by a longer human beat
	pastePause     = 1.0      // each side of a paste: copy before, hands-back after
	pasteFrom      = 25       // inline `spans` this long get pasted, not typed
	tokenFrom      = 30       // bare unbroken runs this long read as pastes too
	readingPause   = 2.0      // the human reads the reply before typing again
	autoSendPause  = 1.2      // auto mode: the beat before "pressing" Return
	wordInterval   = 0.012    // per-word delay while streaming prose
)

var spinnerVerbs = []string{"Thinking", "Pondering", "Rummaging", "Scheming", "Noodling"}
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

var (
	errQuit        = errors.New("quit")
	errInterrupted = errors.New("interrupted")
)

func keystrokeSeconds() float64 { return 60.0 / (typingWPM * 5) } // 5 chars = one word

type player struct {
	ctx  context.Context
	out  io.Writer
	auto bool
}

// play performs a script from the top. It returns errInterrupted on
// Ctrl-C and errQuit when the operator typed q at a prompt; either way
// the project files are handed over first. keep=false removes the temp
// project dir afterwards (looping demos nobody will poke).
func play(s *Script, auto, keep bool) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	workdir, err := os.MkdirTemp("", "fas-"+s.Name+"-")
	if err != nil {
		return err
	}
	p := &player{ctx: ctx, out: os.Stdout, auto: auto}
	err = p.run(s.Events, workdir)
	fmt.Fprint(p.out, showCursor)
	if errors.Is(err, errInterrupted) {
		fmt.Fprintln(p.out, "\n"+styleDim.Render("⏹ interrupted"))
	}
	if s.Tarball != nil {
		if terr := extractTarball(s.Tarball, workdir); terr != nil {
			fmt.Fprintln(p.out, styleError.Render("couldn't unpack the project files: "+terr.Error()))
		}
	}
	if keep {
		fmt.Fprintln(p.out, styleDim.Render("The project files are real - have a poke: "+workdir))
	} else {
		os.RemoveAll(workdir)
	}
	return err
}

func (p *player) run(events []Event, workdir string) error {
	var prev *float64
	for _, e := range events {
		switch e.Type {
		case "meta":
			continue
		case "prompt":
			// No recorded-gap wait here: the pause before a prompt is
			// the operator's own, ended by their Return.
			if err := p.presentPrompt(e.Text); err != nil {
				return err
			}
			t := e.T
			prev = &t
			continue
		}

		gap := pacedGap(e.T, prev)
		var err error
		if gap >= spinnerFrom {
			err = p.thinkingBeat(gap)
		} else {
			err = p.sleep(gap)
		}
		if err != nil {
			return err
		}
		t := e.T
		prev = &t

		switch e.Type {
		case "text":
			fmt.Fprintln(p.out)
			if err := p.streamProse(e.Text); err != nil {
				return err
			}
		case "tool_call":
			fmt.Fprint(p.out, renderToolCall(e))
			materialise(e, workdir)
		case "tool_result":
			fmt.Fprint(p.out, renderResult(e))
		case "turn_end":
			fmt.Fprintln(p.out)
		}
		// "thinking" renders nothing: the gap before it IS the beat.
	}
	return nil
}

func (p *player) sleep(seconds float64) error {
	timer := time.NewTimer(time.Duration(seconds * float64(time.Second)))
	defer timer.Stop()
	select {
	case <-p.ctx.Done():
		return errInterrupted
	case <-timer.C:
		return nil
	}
}

func jitter() float64 { return 0.7 + rand.Float64()*0.6 }

// presentPrompt types the recorded prompt at the ❯. Pastes land as a
// beat for the off-stage copy, then the whole chunk at once - nobody
// types a stack trace character by character.
func (p *player) presentPrompt(text string) error {
	fmt.Fprint(p.out, stylePrompt.Render("❯")+" ")
	if err := p.sleep(readingPause * jitter()); err != nil {
		return err
	}
	keystroke := keystrokeSeconds()
	for _, seg := range segments(text) {
		if seg.paste {
			if err := p.sleep(pastePause * jitter()); err != nil {
				return err
			}
			fmt.Fprint(p.out, seg.text)
			if err := p.sleep(pastePause * jitter()); err != nil {
				return err
			}
			continue
		}
		for _, r := range seg.text {
			fmt.Fprint(p.out, string(r))
			pause := keystroke * (typeJitterLow + rand.Float64()*(typeJitterHigh-typeJitterLow))
			if strings.ContainsRune(pauseKeys, r) {
				pause += keystroke * 3
			}
			if err := p.sleep(pause); err != nil {
				return err
			}
		}
	}
	if p.auto {
		if err := p.sleep(autoSendPause * jitter()); err != nil {
			return err
		}
		fmt.Fprintln(p.out)
		return nil
	}
	reply, err := readLine(p.ctx)
	if err != nil {
		return err
	}
	if isQuit(reply) {
		return errQuit
	}
	return nil
}

func isQuit(reply string) bool {
	switch strings.ToLower(strings.TrimSpace(reply)) {
	case "q", "quit", "exit":
		return true
	}
	return false
}

// readLine waits for Return without leaving a goroutine blocked on
// stdin afterwards: a stray reader would steal the picker's keystrokes.
func readLine(ctx context.Context) (string, error) {
	r, err := cancelreader.NewReader(os.Stdin)
	if err != nil {
		return "", err
	}
	defer r.Close()
	type result struct {
		line string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		var line []byte
		buf := make([]byte, 1)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				if buf[0] == '\n' {
					done <- result{string(line), nil}
					return
				}
				line = append(line, buf[0])
			}
			if err != nil {
				done <- result{string(line), err}
				return
			}
		}
	}()
	select {
	case <-ctx.Done():
		r.Cancel()
		<-done
		return "", errInterrupted
	case got := <-done:
		if got.err != nil {
			fmt.Println()
			return "", errQuit // Ctrl-D or a closed stdin
		}
		return got.line, nil
	}
}

// streamProse prints word by word so it reads as generated, not pasted.
func (p *player) streamProse(text string) error {
	for line := range strings.SplitSeq(text, "\n") {
		words := strings.Fields(line)
		for i, word := range words {
			if i < len(words)-1 {
				word += " "
			}
			fmt.Fprint(p.out, word)
			if err := p.sleep(wordInterval); err != nil {
				return err
			}
		}
		fmt.Fprintln(p.out)
	}
	return nil
}

const (
	hideCursor = "\x1b[?25l"
	showCursor = "\x1b[?25h"
	clearLine  = "\r\x1b[2K"
)

// thinkingBeat is the spinner pause that makes speed legible.
func (p *player) thinkingBeat(seconds float64) error {
	label := styleBold.Render("✳ " + spinnerVerbs[rand.IntN(len(spinnerVerbs))] + "…")
	frame := 0
	draw := func() {
		fmt.Fprint(p.out, clearLine+styleAccent.Render(spinnerFrames[frame%len(spinnerFrames)])+" "+label)
	}
	fmt.Fprint(p.out, hideCursor)
	draw()
	ticker := time.NewTicker(80 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(time.Duration(seconds * float64(time.Second)))
	defer timer.Stop()
	for {
		select {
		case <-p.ctx.Done():
			fmt.Fprint(p.out, clearLine+showCursor)
			return errInterrupted
		case <-timer.C:
			fmt.Fprint(p.out, clearLine+showCursor)
			return nil
		case <-ticker.C:
			frame++
			draw()
		}
	}
}

// --- Prompt segmentation ----------------------------------------------------

type segment struct {
	paste bool
	text  string
}

var (
	pasteRE = regexp.MustCompile("(?s)```.*?```|`[^`\n]+`|[^\\s`]{" + fmt.Sprint(tokenFrom) + ",}")
	langRE  = regexp.MustCompile(`^[\w+-]*$`) // a fence's language tag: one bare word at most
)

// segments splits a prompt into typed and pasted pieces. Fenced blocks
// paste their body - the fences and any language tag are still typed,
// because that's how the real dance goes. Inline `spans` paste only when
// long enough to plausibly be a paste rather than a typed `word`. A bare
// unbroken run of tokenFrom+ characters (a path, a URL, a UUID - no
// human rattles those off) pastes too, with trailing punctuation typed.
func segments(text string) []segment {
	var parts []segment
	typed := func(s string) { parts = append(parts, segment{false, s}) }
	pasted := func(s string) { parts = append(parts, segment{true, s}) }

	cursor := 0
	for _, loc := range pasteRE.FindAllStringIndex(text, -1) {
		chunk := text[loc[0]:loc[1]]
		lead := text[cursor:loc[0]]
		cursor = loc[1]

		var body, ticks string
		switch {
		case strings.HasPrefix(chunk, "```"):
			inner := chunk[3 : len(chunk)-3]
			tag, rest, multiline := strings.Cut(inner, "\n")
			if multiline && langRE.MatchString(tag) {
				typed(lead + "```" + tag + "\n")
				pasted(rest)
				typed("```")
				continue
			}
			if multiline {
				typed(lead + "```")
				pasted(inner)
				typed("```")
				continue
			}
			body, ticks = tag, "```" // one-line fence: judge it like an inline span
		case !strings.HasPrefix(chunk, "`"):
			// a bare run: paste it, but trailing punctuation is grammar
			// the human types ("... the file is /path/to/thing.jsonl.")
			kept := strings.TrimRight(chunk, ".,;:!?\"')")
			typed(lead)
			pasted(kept)
			typed(chunk[len(kept):])
			continue
		default:
			body, ticks = chunk[1:len(chunk)-1], "`"
		}
		if utf8.RuneCountInString(body) >= pasteFrom {
			typed(lead + ticks)
			pasted(body)
			typed(ticks)
		} else {
			typed(lead + chunk)
		}
	}
	typed(text[cursor:])
	return parts
}

// --- Making it real ---------------------------------------------------------

// resolveInside maps a recorded path into the workdir, refusing absolute
// paths and escapees. Recorded paths are relative ("./haido.py").
func resolveInside(workdir, path string) (string, bool) {
	if path == "" || filepath.IsAbs(path) || strings.HasPrefix(path, "~") || strings.HasPrefix(path, "/") {
		return "", false
	}
	target := filepath.Join(workdir, filepath.FromSlash(path))
	rel, err := filepath.Rel(workdir, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return target, true
}

// materialise makes a Write/Edit/apply_patch call real in the workdir.
func materialise(e Event, workdir string) {
	target, ok := resolveInside(workdir, str(e.Input, "file_path"))
	if !ok {
		return
	}
	_, hasContent := e.Input["content"]
	switch {
	case e.Name == "Write" || (e.Name == "apply_patch" && hasContent):
		os.MkdirAll(filepath.Dir(target), 0o755)
		os.WriteFile(target, []byte(str(e.Input, "content")), 0o644)
	case e.Name == "apply_patch" && e.Input["delete"] == true:
		os.Remove(target)
	// apply_patch updates carry only a display diff, not the new file
	// content, so they aren't materialised.
	case e.Name == "Edit":
		data, err := os.ReadFile(target)
		old := str(e.Input, "old_string")
		if err != nil || old == "" {
			return
		}
		count := 1
		if e.Input["replace_all"] == true {
			count = -1
		}
		updated := strings.Replace(string(data), old, str(e.Input, "new_string"), count)
		os.WriteFile(target, []byte(updated), 0o644)
	}
}

// extractTarball unpacks a demo's final project files over the workdir.
// Only plain files and directories, and only inside the workdir.
func extractTarball(data []byte, workdir string) error {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target, ok := resolveInside(workdir, hdr.Name)
		if !ok {
			continue
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				return err
			}
			_, err = io.Copy(f, tr)
			f.Close()
			if err != nil {
				return err
			}
		}
	}
}

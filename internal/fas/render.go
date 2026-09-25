package fas

import (
	"fmt"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

const (
	resultLines  = 4  // tool-result lines shown before "… +N lines"
	excerptLines = 8  // head of a Write shown as code
	diffLines    = 12 // cap on a rendered Edit diff
	diffContext  = 3  // unchanged lines kept either side of a change
	targetWidth  = 68 // cap on the ⏺ Tool(target) line
	indent       = "     "
)

// Plain ANSI colours, so the playback wears the viewer's own terminal
// theme the way a real agent CLI does.
var (
	styleBold   = lipgloss.NewStyle().Bold(true)
	styleDim    = lipgloss.NewStyle().Faint(true)
	styleError  = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	styleTool   = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	styleAccent = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	stylePrompt = lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true)
)

// toolTarget is the bit in brackets on the ⏺ line, kept to one tidy line.
func toolTarget(e Event) string {
	command := str(e.Input, "command")
	if command == "" {
		command = str(e.Input, "cmd")
	}
	if command = strings.TrimSpace(command); command != "" {
		// Bash, codex exec_command, codex JS exec blobs: first line only.
		first, _, multiline := strings.Cut(command, "\n")
		if runes := []rune(first); multiline || len(runes) > targetWidth {
			first = strings.TrimRight(string(runes[:min(len(runes), targetWidth)]), " ") + "…"
		}
		return first
	}
	for _, key := range []string{"file_path", "pattern", "path", "url", "query"} {
		if v, ok := e.Input[key]; ok {
			return fmt.Sprint(v)
		}
	}
	return ""
}

func renderToolCall(e Event) string {
	var b strings.Builder
	b.WriteString(styleTool.Render("⏺") + " " + styleBold.Render(e.Name) + "(" + toolTarget(e) + ")\n")

	_, hasContent := e.Input["content"]
	switch {
	case e.Name == "Write" || (e.Name == "apply_patch" && hasContent):
		lines := strings.Split(strings.TrimRight(str(e.Input, "content"), "\n"), "\n")
		head := strings.Join(lines[:min(len(lines), excerptLines)], "\n")
		b.WriteString(indentBlock(highlight(head, str(e.Input, "file_path"))))
		if len(lines) > excerptLines {
			b.WriteString(styleDim.Render(fmt.Sprintf("%s… +%d lines", indent, len(lines)-excerptLines)) + "\n")
		}
	case e.Name == "apply_patch" && str(e.Input, "diff") != "":
		lines := strings.Split(str(e.Input, "diff"), "\n")
		b.WriteString(indentBlock(highlight(strings.Join(lines[:min(len(lines), diffLines)], "\n"), "x.diff")))
	case e.Name == "Edit":
		diff := lineDiff(splitLines(str(e.Input, "old_string")), splitLines(str(e.Input, "new_string")))
		if len(diff) > 0 {
			b.WriteString(indentBlock(highlight(strings.Join(diff[:min(len(diff), diffLines)], "\n"), "x.diff")))
		}
	}
	return b.String()
}

func renderResult(e Event) string {
	style := styleDim
	if e.IsError {
		style = styleError
	}
	lines := strings.Split(strings.TrimRight(e.Content, " \t\r\n"), "\n")
	var b strings.Builder
	b.WriteString(style.Render("  ⎿  "+lines[0]) + "\n")
	for _, line := range lines[1:min(len(lines), resultLines)] {
		b.WriteString(style.Render(indent+line) + "\n")
	}
	if len(lines) > resultLines {
		b.WriteString(styleDim.Render(fmt.Sprintf("%s… +%d lines", indent, len(lines)-resultLines)) + "\n")
	}
	return b.String()
}

func indentBlock(s string) string {
	var b strings.Builder
	for line := range strings.SplitSeq(strings.TrimRight(s, "\n"), "\n") {
		b.WriteString(indent + line + "\n")
	}
	return b.String()
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// highlight syntax-colours code, choosing a lexer from the filename and
// falling back to sniffing the content. Colour is skipped entirely when
// stdout isn't a colour terminal.
func highlight(code, filename string) string {
	if lipgloss.ColorProfile() == termenv.Ascii {
		return code
	}
	lexer := lexers.Match(filename)
	if lexer == nil {
		lexer = lexers.Analyse(code)
	}
	if lexer == nil {
		lexer = lexers.Fallback
	}
	iterator, err := chroma.Coalesce(lexer).Tokenise(nil, code)
	if err != nil {
		return code
	}
	var b strings.Builder
	if err := formatters.TTY256.Format(&b, codeStyle, iterator); err != nil {
		return code
	}
	return b.String()
}

// codeStyle is monokai with its background stripped, so code sits on
// the terminal's own background instead of a painted slab.
var codeStyle = func() *chroma.Style {
	style, err := styles.Get("monokai").Builder().Transform(func(e chroma.StyleEntry) chroma.StyleEntry {
		e.Background = 0
		return e
	}).Build()
	if err != nil {
		return styles.Fallback
	}
	return style
}()

// lineDiff is a minimal unified-style diff ("-old", "+new", " same") of
// two small snippets, with unchanged lines beyond diffContext trimmed
// from either end. Edit snippets are small, so plain LCS is plenty.
func lineDiff(a, b []string) []string {
	n, m := len(a), len(b)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}

	var out []string
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && a[i] == b[j]:
			out = append(out, " "+a[i])
			i++
			j++
		case i < n && (j == m || lcs[i+1][j] >= lcs[i][j+1]):
			out = append(out, "-"+a[i]) // removals first, as diffs read
			i++
		default:
			out = append(out, "+"+b[j])
			j++
		}
	}

	first, last := -1, -1
	for k, line := range out {
		if line[0] != ' ' {
			if first < 0 {
				first = k
			}
			last = k
		}
	}
	if first < 0 {
		return nil
	}
	return out[max(first-diffContext, 0):min(last+diffContext+1, len(out))]
}

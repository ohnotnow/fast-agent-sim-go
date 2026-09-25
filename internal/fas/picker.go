package fas

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// pick shows the scripts and returns the chosen one, or nil on quit.
func pick(title string, scripts []*Script, cursor int) (*Script, int, error) {
	m := pickerModel{title: title, items: scripts, cursor: cursor}
	m.applyFilter()
	final, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	if err != nil {
		return nil, 0, err
	}
	fm := final.(pickerModel)
	return fm.chosen, fm.cursor, nil
}

var (
	pickTitle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	pickSelected = lipgloss.NewStyle().Bold(true)
	pickFaint    = lipgloss.NewStyle().Faint(true)
	pickTag      = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))
)

type pickerModel struct {
	title     string
	items     []*Script
	visible   []int // indexes into items that pass the filter
	cursor    int   // position within visible
	offset    int   // first visible row on screen
	filter    string
	filtering bool
	chosen    *Script
	width     int
	height    int
}

func (m pickerModel) Init() tea.Cmd { return nil }

func (m *pickerModel) applyFilter() {
	words := strings.Fields(strings.ToLower(m.filter))
	m.visible = m.visible[:0]
	for i, s := range m.items {
		text := s.searchText()
		keep := true
		for _, w := range words {
			if !strings.Contains(text, w) {
				keep = false
				break
			}
		}
		if keep {
			m.visible = append(m.visible, i)
		}
	}
	m.cursor = min(m.cursor, max(len(m.visible)-1, 0))
}

func (m pickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyMsg:
		if m.filtering {
			return m.updateFilter(msg)
		}
		switch key := msg.String(); key {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		case "j", "down":
			m.cursor = min(m.cursor+1, max(len(m.visible)-1, 0))
		case "k", "up":
			m.cursor = max(m.cursor-1, 0)
		case "g", "home":
			m.cursor = 0
		case "G", "end":
			m.cursor = max(len(m.visible)-1, 0)
		case "pgdown", "ctrl+d":
			m.cursor = min(m.cursor+m.rows(), max(len(m.visible)-1, 0))
		case "pgup", "ctrl+u":
			m.cursor = max(m.cursor-m.rows(), 0)
		case "/":
			m.filtering = true
		case "enter":
			if len(m.visible) > 0 {
				m.chosen = m.items[m.visible[m.cursor]]
				return m, tea.Quit
			}
		default:
			if len(key) == 1 && key[0] >= '1' && key[0] <= '9' {
				if n := int(key[0] - '1'); n < len(m.visible) {
					m.cursor = n
				}
			}
		}
	}
	m.scroll()
	return m, nil
}

func (m pickerModel) updateFilter(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		return m, tea.Quit
	case tea.KeyEsc:
		m.filtering, m.filter = false, ""
	case tea.KeyEnter:
		m.filtering = false
	case tea.KeyBackspace:
		if r := []rune(m.filter); len(r) > 0 {
			m.filter = string(r[:len(r)-1])
		}
	case tea.KeyRunes, tea.KeySpace:
		m.filter += string(msg.Runes)
	}
	m.applyFilter()
	m.scroll()
	return m, nil
}

// rows is how many list rows fit between the title and the footer.
func (m pickerModel) rows() int {
	if m.height == 0 {
		return 20
	}
	return max(m.height-5, 1)
}

func (m *pickerModel) scroll() {
	rows := m.rows()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+rows {
		m.offset = m.cursor - rows + 1
	}
}

func (m pickerModel) View() string {
	width := m.width
	if width == 0 {
		width = 80
	}
	var b strings.Builder
	b.WriteString(pickTitle.Render(m.title) + "\n\n")

	if len(m.visible) == 0 {
		b.WriteString(pickFaint.Render("  nothing matches") + "\n")
	}
	end := min(m.offset+m.rows(), len(m.visible))
	for pos := m.offset; pos < end; pos++ {
		b.WriteString(m.renderRow(m.items[m.visible[pos]], pos == m.cursor, width) + "\n")
	}
	for range m.rows() - (end - m.offset) {
		b.WriteString("\n")
	}

	b.WriteString("\n")
	if m.filtering {
		b.WriteString(pickTitle.Render("/") + m.filter + "█")
	} else {
		help := "↑/↓ move · enter play · / filter · q quit"
		if m.filter != "" {
			help = fmt.Sprintf("filter: %q · esc clears · ", m.filter) + help
		}
		b.WriteString(pickFaint.Render(help))
	}
	return b.String()
}

// renderRow lays out "▸ when  tag  prompt…  (timing)" at exactly the
// terminal width, measuring and truncating plain text before styling
// so nested ANSI never confuses the width sums.
func (m pickerModel) renderRow(s *Script, selected bool, width int) string {
	marker := "  "
	if selected {
		marker = "▸ "
	}
	when := "demo        "
	if s.Kind != "demo" {
		when = s.When.Format("02 Jan 15:04")
	}
	tag := ""
	if s.Kind == "codex" {
		tag = "codex "
	}
	title := s.Title
	if s.Kind == "demo" {
		title = s.Title + ": " + s.FirstPrompt()
	}
	timing := s.TimingTag()

	fixed := ansi.StringWidth(marker+when+"  "+tag) + 2 + ansi.StringWidth(timing)
	title = ansi.Truncate(title, max(width-fixed-1, 10), "…")
	gap := max(width-fixed-ansi.StringWidth(title)-1, 1)

	text := title
	if selected {
		text = pickSelected.Render(title)
	}
	return marker + pickFaint.Render(when) + "  " + pickTag.Render(tag) + text +
		strings.Repeat(" ", gap) + pickFaint.Render(timing)
}

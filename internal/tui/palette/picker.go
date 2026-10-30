package palette

import (
	"strings"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"hsh/internal/tui/style"
)

type item struct {
	id   string
	text string
	note string
}

type picker struct {
	input  textinput.Model
	items  []item
	shown  []list.Rank
	cursor int
}

func newTextInput(prompt, placeholder string) textinput.Model {
	in := textinput.New()
	in.Prompt = prompt
	in.Placeholder = placeholder
	in.Cursor.SetMode(cursor.CursorStatic)
	in.Focus()
	return in
}

func newPicker(placeholder string) picker {
	return picker{input: newTextInput("> ", placeholder)}
}

func (p *picker) setItems(items []item) {
	p.items = items
	p.refilter()
}

func (p *picker) reset() {
	p.input.SetValue("")
	p.cursor = 0
	p.refilter()
}

func (p *picker) query() string {
	return strings.TrimSpace(p.input.Value())
}

func (p *picker) refilter() {
	q := p.query()
	if q == "" {
		p.shown = make([]list.Rank, len(p.items))
		for i := range p.items {
			p.shown[i] = list.Rank{Index: i}
		}
	} else {
		targets := make([]string, len(p.items))
		for i, it := range p.items {
			targets[i] = it.text
		}
		p.shown = list.DefaultFilter(q, targets)
	}
	p.cursor = max(0, min(p.cursor, len(p.shown)-1))
}

func (p *picker) selectID(id string) {
	for i, r := range p.shown {
		if p.items[r.Index].id == id {
			p.cursor = i
			return
		}
	}
}

func (p picker) selected() (item, bool) {
	if p.cursor < 0 || p.cursor >= len(p.shown) {
		return item{}, false
	}
	return p.items[p.shown[p.cursor].Index], true
}

func (p *picker) update(msg tea.KeyMsg) tea.Cmd {
	switch msg.Type {
	case tea.KeyUp, tea.KeyCtrlP, tea.KeyShiftTab:
		if p.cursor > 0 {
			p.cursor--
		}
		return nil
	case tea.KeyDown, tea.KeyCtrlN, tea.KeyTab:
		if p.cursor < len(p.shown)-1 {
			p.cursor++
		}
		return nil
	}
	before := p.input.Value()
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)
	if p.input.Value() != before {
		p.cursor = 0
		p.refilter()
	}
	return cmd
}

func (p picker) view(rows int) string {
	rows = max(rows, 1)
	lines := []string{p.input.View(), ""}
	if len(p.shown) == 0 {
		lines = append(lines, noteStyle.Render("  no matches"))
		return strings.Join(lines, "\n")
	}
	start := 0
	if p.cursor >= rows {
		start = p.cursor - rows + 1
	}
	end := min(len(p.shown), start+rows)
	for i := start; i < end; i++ {
		r := p.shown[i]
		it := p.items[r.Index]
		base, match, prefix := plainStyle, matchStyle, "  "
		if i == p.cursor {
			base, match, prefix = selectedStyle, selectedMatchStyle, "▸ "
		}
		line := base.Render(prefix) + lipgloss.StyleRunes(it.text, r.MatchedIndexes, match, base)
		if it.note != "" {
			line += noteStyle.Render("  " + it.note)
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

var (
	plainStyle         = lipgloss.NewStyle()
	matchStyle         = lipgloss.NewStyle().Bold(true).Underline(true)
	selectedStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("212"))
	selectedMatchStyle = selectedStyle.Bold(true).Underline(true)
	noteStyle          = style.Note
	titleStyle         = style.Title
	helpStyle          = style.Help
	errorStyle         = style.Error
)

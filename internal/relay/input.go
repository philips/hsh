package relay

import (
	"bytes"
	"strconv"
	"strings"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
)

var (
	pasteStart = []byte("\x1b[200~")
	pasteEnd   = []byte("\x1b[201~")
)

type inputParser struct {
	pasting bool
	paste   []rune
	held    []byte
}

func (ip *inputParser) feed(p []byte) []tea.KeyMsg {
	data := append(append([]byte(nil), ip.held...), p...)
	ip.held = nil
	var out []tea.KeyMsg
	for len(data) > 0 {
		if ip.pasting {
			j := bytes.Index(data, pasteEnd)
			if j < 0 {
				keep := partialSuffix(data, pasteEnd)
				ip.paste = append(ip.paste, pasteRunes(data[:len(data)-keep])...)
				ip.held = append([]byte(nil), data[len(data)-keep:]...)
				return out
			}
			ip.paste = append(ip.paste, pasteRunes(data[:j])...)
			out = append(out, tea.KeyMsg{Type: tea.KeyRunes, Runes: ip.paste, Paste: true})
			ip.paste, ip.pasting = nil, false
			data = data[j+len(pasteEnd):]
			continue
		}
		j := bytes.Index(data, pasteStart)
		if j < 0 {
			keep := partialSuffix(data, pasteStart)
			out = append(out, parseInput(data[:len(data)-keep])...)
			ip.held = append([]byte(nil), data[len(data)-keep:]...)
			return out
		}
		out = append(out, parseInput(data[:j])...)
		ip.pasting = true
		data = data[j+len(pasteStart):]
	}
	return out
}

func partialSuffix(data, marker []byte) int {
	for n := min(len(marker)-1, len(data)); n >= 2; n-- {
		if bytes.Equal(data[len(data)-n:], marker[:n]) {
			return n
		}
	}
	return 0
}

func pasteRunes(b []byte) []rune {
	text := strings.ReplaceAll(strings.ReplaceAll(string(b), "\r\n", "\n"), "\r", "\n")
	return []rune(text)
}

func parseInput(p []byte) []tea.KeyMsg {
	var out []tea.KeyMsg
	for i := 0; i < len(p); {
		b := p[i]
		switch {
		case b == 0x1b:
			msg, width := parseEscape(p[i:])
			if width == 0 {
				width = 1
			}
			if msg != nil {
				out = append(out, *msg)
			}
			i += width
		case b == '\r' || b == '\n':
			out = append(out, tea.KeyMsg{Type: tea.KeyEnter})
			i++
		case b == '\t':
			out = append(out, tea.KeyMsg{Type: tea.KeyTab})
			i++
		case b == 0x7f || b == 0x08:
			out = append(out, tea.KeyMsg{Type: tea.KeyBackspace})
			i++
		case b == ' ':
			out = append(out, tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
			i++
		case b < 0x20:
			out = append(out, tea.KeyMsg{Type: tea.KeyType(b)})
			i++
		default:
			r, size := utf8.DecodeRune(p[i:])
			if r == utf8.RuneError && size <= 1 {
				i++
				continue
			}
			out = append(out, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
			i += size
		}
	}
	return out
}

func parseEscape(p []byte) (*tea.KeyMsg, int) {
	if len(p) == 1 {
		return &tea.KeyMsg{Type: tea.KeyEsc}, 1
	}
	switch p[1] {
	case '[':
		if len(p) >= 3 && p[2] == 'M' {
			return nil, min(len(p), 6)
		}
		return parseCSI(p)
	case 'O':
		if len(p) >= 3 {
			if t, ok := arrowType(p[2]); ok {
				return &tea.KeyMsg{Type: t}, 3
			}
			return nil, 3
		}
		return nil, len(p)
	}
	r, size := utf8.DecodeRune(p[1:])
	if r == utf8.RuneError {
		return &tea.KeyMsg{Type: tea.KeyEsc}, 1
	}
	return &tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}, Alt: true}, 1 + size
}

func arrowType(b byte) (tea.KeyType, bool) {
	switch b {
	case 'A':
		return tea.KeyUp, true
	case 'B':
		return tea.KeyDown, true
	case 'C':
		return tea.KeyRight, true
	case 'D':
		return tea.KeyLeft, true
	case 'H':
		return tea.KeyHome, true
	case 'F':
		return tea.KeyEnd, true
	}
	return 0, false
}

func parseCSI(p []byte) (*tea.KeyMsg, int) {
	end := 2
	for end < len(p) && p[end] >= 0x20 && p[end] <= 0x3f {
		end++
	}
	if end >= len(p) {
		return nil, len(p)
	}
	final := p[end]
	width := end + 1
	params := strings.Split(string(p[2:end]), ";")
	switch final {
	case 'A', 'B', 'C', 'D', 'H', 'F':
		t, _ := arrowType(final)
		return &tea.KeyMsg{Type: t}, width
	case '~':
		switch params[0] {
		case "1", "7":
			return &tea.KeyMsg{Type: tea.KeyHome}, width
		case "4", "8":
			return &tea.KeyMsg{Type: tea.KeyEnd}, width
		case "3":
			return &tea.KeyMsg{Type: tea.KeyDelete}, width
		case "5":
			return &tea.KeyMsg{Type: tea.KeyPgUp}, width
		case "6":
			return &tea.KeyMsg{Type: tea.KeyPgDown}, width
		}
	case 'u':
		return parseKittyKey(params), width
	}
	return nil, width
}

func parseKittyKey(params []string) *tea.KeyMsg {
	code, err := strconv.Atoi(strings.SplitN(params[0], ":", 2)[0])
	if err != nil {
		return nil
	}
	mods := 0
	if len(params) > 1 {
		parts := strings.SplitN(params[1], ":", 2)
		if m, err := strconv.Atoi(parts[0]); err == nil && m >= 1 {
			mods = m - 1
		}
		if len(parts) == 2 && parts[1] == "3" {
			return nil
		}
	}
	shift, alt, ctrl := mods&1 != 0, mods&2 != 0, mods&4 != 0
	switch code {
	case 27:
		return &tea.KeyMsg{Type: tea.KeyEsc, Alt: alt}
	case 13:
		return &tea.KeyMsg{Type: tea.KeyEnter, Alt: alt}
	case 9:
		if shift {
			return &tea.KeyMsg{Type: tea.KeyShiftTab, Alt: alt}
		}
		return &tea.KeyMsg{Type: tea.KeyTab, Alt: alt}
	case 127:
		return &tea.KeyMsg{Type: tea.KeyBackspace, Alt: alt}
	case 32:
		if ctrl {
			return &tea.KeyMsg{Type: tea.KeyCtrlAt}
		}
		return &tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}, Alt: alt}
	}
	if ctrl && code >= 'a' && code <= 'z' {
		return &tea.KeyMsg{Type: tea.KeyType(code - 'a' + 1), Alt: alt}
	}
	if code < 32 || code > 0x10ffff {
		return nil
	}
	r := rune(code)
	if shift && r >= 'a' && r <= 'z' {
		r = r - 'a' + 'A'
	}
	return &tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}, Alt: alt}
}

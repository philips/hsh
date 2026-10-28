package relay

import (
	"fmt"
	"strings"
)

type Key struct {
	Byte  byte
	Kitty []byte
}

func ParseKey(name string) (Key, error) {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" {
		n = "ctrl-space"
	}
	rest, ok := strings.CutPrefix(n, "ctrl-")
	if !ok {
		return Key{}, fmt.Errorf("monitor key %q: want ctrl-space, ctrl-a..ctrl-z, ctrl-\\, ctrl-], ctrl-^ or ctrl-_", name)
	}
	k, err := parseKeyBody(name, rest)
	if err != nil {
		return Key{}, err
	}
	switch k.Byte {
	case 0x08, 0x09, 0x0a, 0x0d:
		return Key{}, fmt.Errorf("monitor key %q is Backspace, Tab, or Enter and cannot be used", name)
	}
	return k, nil
}

func parseKeyBody(name, rest string) (Key, error) {
	switch {
	case rest == "space" || rest == "@":
		return Key{Byte: 0x00, Kitty: []byte("\x1b[32;5u")}, nil
	case len(rest) == 1 && rest[0] >= 'a' && rest[0] <= 'z':
		return Key{Byte: rest[0] - 'a' + 1}, nil
	case rest == "\\":
		return Key{Byte: 0x1c}, nil
	case rest == "]":
		return Key{Byte: 0x1d}, nil
	case rest == "^":
		return Key{Byte: 0x1e}, nil
	case rest == "_":
		return Key{Byte: 0x1f}, nil
	}
	return Key{}, fmt.Errorf("monitor key %q: want ctrl-space, ctrl-a..ctrl-z, ctrl-\\, ctrl-], ctrl-^ or ctrl-_", name)
}

func (k Key) find(p []byte) (idx, width int) {
	idx = -1
	for i, b := range p {
		if b == k.Byte {
			idx, width = i, 1
			break
		}
	}
	if len(k.Kitty) > 0 {
		if j := indexSeq(p, k.Kitty); j >= 0 && (idx < 0 || j < idx) {
			idx, width = j, len(k.Kitty)
		}
	}
	return
}

func indexSeq(p, seq []byte) int {
	for i := 0; i+len(seq) <= len(p); i++ {
		if string(p[i:i+len(seq)]) == string(seq) {
			return i
		}
	}
	return -1
}

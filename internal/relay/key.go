package relay

import (
	"fmt"
	"strconv"
	"strings"
)

type Key struct {
	Byte byte
	Code rune
}

const keyHelp = "want ctrl-space, ctrl-a..ctrl-z, ctrl-\\, ctrl-], ctrl-^ or ctrl-_"

func ParseKey(name string) (Key, error) {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" {
		n = "ctrl-space"
	}
	rest, ok := strings.CutPrefix(n, "ctrl-")
	if !ok {
		return Key{}, fmt.Errorf("monitor key %q: %s", name, keyHelp)
	}
	k, ok := ctrlKey(rest)
	if !ok {
		return Key{}, fmt.Errorf("monitor key %q: %s", name, keyHelp)
	}
	switch k.Byte {
	case 0x08, 0x09, 0x0a, 0x0d:
		return Key{}, fmt.Errorf("monitor key %q is Backspace, Tab, or Enter and cannot be used", name)
	}
	return k, nil
}

func ctrlKey(rest string) (Key, bool) {
	switch {
	case rest == "space" || rest == "@":
		return Key{Byte: 0x00, Code: ' '}, true
	case len(rest) == 1 && rest[0] >= 'a' && rest[0] <= 'z':
		return Key{Byte: rest[0] - 'a' + 1, Code: rune(rest[0])}, true
	case rest == "\\":
		return Key{Byte: 0x1c, Code: '\\'}, true
	case rest == "]":
		return Key{Byte: 0x1d, Code: ']'}, true
	case rest == "^":
		return Key{Byte: 0x1e, Code: '^'}, true
	case rest == "_":
		return Key{Byte: 0x1f, Code: '_'}, true
	}
	return Key{}, false
}

func ParseDetachKeys(spec string) []Key {
	var keys []Key
	for _, tok := range strings.Split(spec, ",") {
		rest, ok := strings.CutPrefix(strings.ToLower(strings.TrimSpace(tok)), "ctrl-")
		if !ok {
			continue
		}
		if k, ok := ctrlKey(rest); ok {
			keys = append(keys, k)
		}
	}
	return keys
}

type ctrlEvent struct {
	code    rune
	width   int
	release bool
}

func decodeCtrl(p []byte) (ctrlEvent, bool) {
	if len(p) < 4 || p[0] != 0x1b || p[1] != '[' {
		return ctrlEvent{}, false
	}
	end := 2
	for end < len(p) && p[end] >= 0x30 && p[end] <= 0x3f {
		end++
	}
	if end >= len(p) {
		return ctrlEvent{}, false
	}
	final := p[end]
	params := strings.Split(string(p[2:end]), ";")
	var codeStr, modStr string
	switch {
	case final == 'u' && len(params) >= 2:
		codeStr, modStr = params[0], params[1]
	case final == '~' && len(params) == 3 && params[0] == "27":
		modStr, codeStr = params[1], params[2]
	default:
		return ctrlEvent{}, false
	}
	code, err := strconv.Atoi(strings.SplitN(codeStr, ":", 2)[0])
	if err != nil {
		return ctrlEvent{}, false
	}
	modParts := strings.SplitN(modStr, ":", 2)
	mods, err := strconv.Atoi(modParts[0])
	if err != nil || mods < 1 {
		return ctrlEvent{}, false
	}
	bits := mods - 1
	if bits&4 == 0 || bits&^(4|64|128) != 0 {
		return ctrlEvent{}, false
	}
	release := len(modParts) == 2 && modParts[1] == "3"
	return ctrlEvent{code: rune(code), width: end + 1, release: release}, true
}

func (k Key) find(p []byte) (idx, width int) {
	for i := 0; i < len(p); i++ {
		if p[i] == k.Byte {
			return i, 1
		}
		if p[i] == 0x1b {
			if ev, ok := decodeCtrl(p[i:]); ok && ev.code == k.Code && !ev.release {
				return i, ev.width
			}
		}
	}
	return -1, 0
}

func translateKeys(p []byte, keys []Key) []byte {
	if len(keys) == 0 {
		return p
	}
	var out []byte
	for i := 0; i < len(p); {
		if p[i] == 0x1b {
			if ev, ok := decodeCtrl(p[i:]); ok {
				if k, found := matchCode(keys, ev.code); found {
					if !ev.release {
						out = append(out, k.Byte)
					}
					i += ev.width
					continue
				}
			}
		}
		out = append(out, p[i])
		i++
	}
	return out
}

func matchCode(keys []Key, code rune) (Key, bool) {
	for _, k := range keys {
		if k.Code == code {
			return k, true
		}
	}
	return Key{}, false
}

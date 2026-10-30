package relay

import (
	"bytes"
	"strconv"
	"strings"
	"testing"
)

func validKeys() []Key {
	var names []string
	names = append(names, "ctrl-space")
	for c := 'a'; c <= 'z'; c++ {
		names = append(names, "ctrl-"+string(c))
	}
	names = append(names, `ctrl-\`, "ctrl-^", "ctrl-_")
	var keys []Key
	for _, n := range names {
		k, err := ParseKey(n)
		if err != nil {
			continue
		}
		keys = append(keys, k)
	}
	return keys
}

func FuzzKeyFind(f *testing.F) {
	f.Add([]byte("\x1b[1;1R"))
	f.Add([]byte("\x00"))
	f.Add([]byte("\x1b[32;5u"))
	f.Add([]byte("\x1b[27;5;32~"))
	f.Add([]byte("abc\x01def"))
	f.Add([]byte("\x1b[93;5:3u"))
	f.Add([]byte("\x1b[?1;0;2004u"))
	f.Add([]byte("\x1b[27;5;97~x"))
	f.Add([]byte("\x1b]7777;hint\007"))
	f.Fuzz(func(t *testing.T, p []byte) {
		for _, k := range validKeys() {
			idx, width := k.find(p)
			if idx < 0 {
				continue
			}
			if idx >= len(p) || width < 1 || idx+width > len(p) {
				t.Fatalf("key byte %d: match out of range idx=%d width=%d len=%d", k.Byte, idx, width, len(p))
			}
			if !genuineMatch(k, p, idx) {
				t.Fatalf("key byte %d matched a non-genuine encoding at %d in %q", k.Byte, idx, p)
			}
		}
	})
}

func FuzzInputParser(f *testing.F) {
	f.Add([]byte("hello"), 0)
	f.Add([]byte("\x1b[32;5u"), 3)
	f.Add([]byte("\x1b[200~x\x1b[201~"), 4)
	f.Add([]byte("\x1b[A"), 1)
	f.Add([]byte("\x1b[M !!j"), 2)
	f.Fuzz(func(t *testing.T, p []byte, split int) {
		s := 0
		if len(p) > 0 {
			s = split % len(p)
			if s < 0 {
				s += len(p)
			}
		}
		var ip inputParser
		ip.feed(p[:s])
		ip.feed(p[s:])
		if bytes.IndexByte(p, 0x1b) < 0 {
			var whole inputParser
			same := func(q []byte) string {
				var out []string
				for _, m := range whole.feed(q) {
					out = append(out, m.String())
				}
				return strings.Join(out, "|")
			}
			want := same(p)
			var split2 inputParser
			var got []string
			for _, m := range split2.feed(p[:s]) {
				got = append(got, m.String())
			}
			for _, m := range split2.feed(p[s:]) {
				got = append(got, m.String())
			}
			if strings.Join(got, "|") != want {
				t.Fatalf("split feed diverged on escape-free input %q: %q vs %q", p, strings.Join(got, "|"), want)
			}
		}
	})
}

func FuzzDecodeCtrl(f *testing.F) {
	f.Add([]byte("\x1b[32;5u"))
	f.Add([]byte("\x1b[27;5;93~"))
	f.Add([]byte("\x1b[93;5:3u"))
	f.Add([]byte("\x1b[?1;0;2004u"))
	f.Add([]byte("\x1b[99999999999999999999;999999999999u"))
	f.Fuzz(func(t *testing.T, p []byte) {
		ev, ok := decodeCtrl(p)
		if !ok {
			return
		}
		if ev.width < 4 || ev.width > len(p) {
			t.Fatalf("width %d out of range for %q", ev.width, p)
		}
		if p[0] != 0x1b || p[1] != '[' {
			t.Fatalf("matched without a CSI introducer: %q", p)
		}
		ev2, ok2 := decodeCtrl(p[:ev.width])
		if !ok2 || ev2 != ev {
			t.Fatalf("re-decoding the matched slice %q gave %+v not %+v", p[:ev.width], ev2, ev)
		}
	})
}

func FuzzHintFilter(f *testing.F) {
	f.Add([]byte("\x1b]7777;hello\007"), 3)
	f.Add([]byte("\x1b]7777;"), 0)
	f.Add([]byte(strings.Repeat("A", 600)), 100)
	f.Add([]byte("\x1b]7777;a\x1b\\"), 6)
	f.Fuzz(func(t *testing.T, p []byte, split int) {
		s := 0
		if len(p) > 0 {
			s = split % len(p)
			if s < 0 {
				s += len(p)
			}
		}
		var hf hintFilter
		var clean []byte
		c1, _ := hf.feed(p[:s])
		clean = append(clean, c1...)
		c2, _ := hf.feed(p[s:])
		clean = append(clean, c2...)
		if len(hf.held) > len(hintPrefix)+512 {
			t.Fatalf("hint filter holds %d bytes, over the give-up bound", len(hf.held))
		}
		if bytes.IndexByte(p, 0x1b) < 0 && string(clean) != string(p) {
			t.Fatalf("escape-free input was modified: %q -> %q", p, clean)
		}
	})
}

func FuzzRegionFilter(f *testing.F) {
	f.Add([]byte("\x1b[1;50r"), 0, 29)
	f.Add([]byte("\x1b[30;31r"), 0, 29)
	f.Add([]byte("x\x1b[2;"), 0, 29)
	f.Add([]byte("\x1b[99999999999999999999;2r"), 0, 29)
	f.Add([]byte("\x1b[1;1;1;1;1;1;1;1;1"), 17, 29)
	f.Add([]byte("\x1b[1;1;1;1;1;1;1;1;1"), 0, 29)
	f.Add([]byte("1;40r"), 0, 29)
	f.Fuzz(func(t *testing.T, p []byte, split, rows int) {
		rows = rows % 200
		if rows < 1 {
			rows = 1
		}
		s := 0
		if len(p) > 0 {
			s = split % len(p)
			if s < 0 {
				s += len(p)
			}
		}
		var rf regionFilter
		out := append([]byte(nil), rf.feed(p[:s], rows)...)
		out = append(out, rf.feed(p[s:], rows)...)
		if len(rf.held) > 32 {
			t.Fatalf("region filter holds %d bytes after a complete pass", len(rf.held))
		}
		for i := 0; i+1 < len(out); i++ {
			if out[i] != 0x1b || out[i+1] != '[' {
				continue
			}
			j := i + 2
			for j < len(out) && isParamByte(out[j]) {
				j++
			}
			if j >= len(out) || out[j] != 'r' {
				continue
			}
			params := strings.Split(string(out[i+2:j]), ";")
			if len(params) != 2 {
				t.Fatalf("rewritten region has %d params: %q", len(params), out[i:j+1])
			}
			bottom, err := strconv.Atoi(params[1])
			if err != nil || bottom < 1 || bottom > rows {
				t.Fatalf("rewritten region bottom %q escapes the status row (rows=%d)", params[1], rows)
			}
		}
	})
}

package main

import (
	"reflect"
	"testing"
)

func TestOSCFilter(t *testing.T) {
	cases := []struct {
		name   string
		in     []string
		clean  string
		events []string
	}{
		{"plain", []string{"hello"}, "hello", nil},
		{"bel", []string{"a\x1b]7777;approve;x.com\x07b"}, "ab", []string{"approve;x.com"}},
		{"st", []string{"a\x1b]7777;approve;x.com\x1b\\b"}, "ab", []string{"approve;x.com"}},
		{"split", []string{"a\x1b]77", "77;approve;", "x.com\x07b"}, "ab", []string{"approve;x.com"}},
		{"other osc kept", []string{"\x1b]0;title\x07"}, "\x1b]0;title\x07", nil},
		{"csi kept", []string{"\x1b[31mred"}, "\x1b[31mred", nil},
	}
	for _, c := range cases {
		var f oscFilter
		var clean []byte
		var events []string
		for _, in := range c.in {
			cl, ev := f.feed([]byte(in))
			clean = append(clean, cl...)
			events = append(events, ev...)
		}
		if string(clean) != c.clean || !reflect.DeepEqual(events, c.events) {
			t.Errorf("%s: clean=%q events=%v", c.name, clean, events)
		}
	}
}

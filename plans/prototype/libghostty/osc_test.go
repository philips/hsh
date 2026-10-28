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

func TestRegionFilter(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want string
	}{
		{"plain", []string{"hello"}, "hello"},
		{"reset", []string{"a\x1b[rb"}, "a\x1b[1;23rb"},
		{"top only", []string{"\x1b[5r"}, "\x1b[5;23r"},
		{"explicit", []string{"\x1b[2;10r"}, "\x1b[2;10r"},
		{"split", []string{"x\x1b[", "2;", "10r"}, "x\x1b[2;10r"},
		{"other csi kept", []string{"\x1b[31m\x1b[H"}, "\x1b[31m\x1b[H"},
		{"private r kept", []string{"\x1b[?1049r"}, "\x1b[?1049r"},
		{"lone esc held", []string{"a\x1b", "[rb"}, "a\x1b[1;23rb"},
	}
	for _, c := range cases {
		var f regionFilter
		var got []byte
		for _, in := range c.in {
			got = append(got, f.feed([]byte(in), 23)...)
		}
		if string(got) != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

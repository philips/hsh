package main

import (
	"bytes"
	"strconv"
	"strings"
)

type regionFilter struct {
	held []byte
}

func isParamByte(b byte) bool {
	return (b >= '0' && b <= '9') || b == ';'
}

func (f *regionFilter) feed(in []byte, rows int) []byte {
	data := append(f.held, in...)
	f.held = nil
	var out []byte
	for len(data) > 0 {
		i := bytes.IndexByte(data, 0x1b)
		if i < 0 {
			return append(out, data...)
		}
		out = append(out, data[:i]...)
		data = data[i:]
		if len(data) == 1 {
			f.held = []byte{0x1b}
			return out
		}
		if data[1] != '[' {
			out = append(out, data[0])
			data = data[1:]
			continue
		}
		j := 2
		for j < len(data) && isParamByte(data[j]) {
			j++
		}
		if j == len(data) {
			if j-2 > 16 {
				out = append(out, data[0])
				data = data[1:]
				continue
			}
			f.held = append([]byte(nil), data...)
			return out
		}
		if data[j] == 'r' {
			out = append(out, rewriteRegion(string(data[2:j]), rows)...)
		} else {
			out = append(out, data[:j+1]...)
		}
		data = data[j+1:]
	}
	return out
}

func rewriteRegion(params string, rows int) []byte {
	top, bottom := 1, rows
	parts := strings.Split(params, ";")
	if len(parts) > 0 && parts[0] != "" {
		if v, err := strconv.Atoi(parts[0]); err == nil && v > 0 {
			top = v
		}
	}
	if len(parts) > 1 && parts[1] != "" {
		if v, err := strconv.Atoi(parts[1]); err == nil && v > 0 && v < rows {
			bottom = v
		}
	}
	if top >= bottom {
		top = 1
	}
	return []byte("\x1b[" + strconv.Itoa(top) + ";" + strconv.Itoa(bottom) + "r")
}

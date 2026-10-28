package main

import "bytes"

const oscPrefix = "\x1b]7777;"

type oscFilter struct {
	held []byte
}

func (f *oscFilter) feed(in []byte) (clean []byte, events []string) {
	data := append(f.held, in...)
	f.held = nil
	for len(data) > 0 {
		i := bytes.IndexByte(data, 0x1b)
		if i < 0 {
			clean = append(clean, data...)
			return
		}
		clean = append(clean, data[:i]...)
		data = data[i:]
		switch {
		case len(data) < len(oscPrefix) && bytes.HasPrefix([]byte(oscPrefix), data):
			f.held = append([]byte(nil), data...)
			return
		case !bytes.HasPrefix(data, []byte(oscPrefix)):
			clean = append(clean, data[0])
			data = data[1:]
			continue
		}
		body := data[len(oscPrefix):]
		end, width := -1, 0
		for j, b := range body {
			if b == 0x07 {
				end, width = j, 1
				break
			}
			if b == 0x1b && j+1 < len(body) && body[j+1] == '\\' {
				end, width = j, 2
				break
			}
			if b == 0x1b && j+1 >= len(body) {
				break
			}
		}
		if end < 0 {
			if len(body) > 512 {
				clean = append(clean, data[0])
				data = data[1:]
				continue
			}
			f.held = append([]byte(nil), data...)
			return
		}
		events = append(events, string(body[:end]))
		data = body[end+width:]
	}
	return
}

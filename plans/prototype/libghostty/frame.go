package main

import (
	"encoding/binary"
	"io"
)

const (
	frameInput  = 'I'
	frameOutput = 'O'
	frameResize = 'R'
	frameEnd    = 'E'
)

func writeFrame(w io.Writer, kind byte, payload []byte) error {
	hdr := make([]byte, 5)
	hdr[0] = kind
	binary.BigEndian.PutUint32(hdr[1:], uint32(len(payload)))
	_, err := w.Write(append(hdr, payload...))
	return err
}

func readFrame(r io.Reader) (byte, []byte, error) {
	hdr := make([]byte, 5)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return 0, nil, err
	}
	payload := make([]byte, binary.BigEndian.Uint32(hdr[1:]))
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	return hdr[0], payload, nil
}

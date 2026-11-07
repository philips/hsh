//go:build goexperiment.simd

package libghostty_test

import (
	"errors"
	"testing"

	"hsh/internal/vt/libghostty"
)

func newTerminal(t *testing.T, cols, rows uint16) *libghostty.Terminal {
	t.Helper()

	term, err := libghostty.NewTerminal(
		libghostty.WithSize(cols, rows),
		libghostty.WithMaxScrollbackLines(1000),
		libghostty.WithContinuationMaxBytes(65<<20),
	)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(term.Close)

	return term
}

func formatTerminal(t *testing.T, term *libghostty.Terminal, opts ...libghostty.FormatterOption) []byte {
	t.Helper()

	formatter, err := libghostty.NewFormatter(term, opts...)
	if err != nil {
		t.Fatal(err)
	}
	defer formatter.Close()

	data, err := formatter.Format()
	if err != nil {
		t.Fatal(err)
	}

	return data
}

func TestTerminal(t *testing.T) {
	term := newTerminal(t, 80, 24)

	written, err := term.Write([]byte("hello\r\nworld"))
	if err != nil {
		t.Fatal(err)
	}
	if written != len("hello\r\nworld") {
		t.Fatalf("wrote %d bytes", written)
	}

	formatted := formatTerminal(
		t,
		term,
		libghostty.WithFormatterFormat(libghostty.FormatterFormatPlain),
		libghostty.WithFormatterTrim(true),
	)
	if string(formatted) != "hello\nworld" {
		t.Fatalf("formatted %q", formatted)
	}

	cols, err := term.Cols()
	if err != nil {
		t.Fatal(err)
	}
	if cols != 80 {
		t.Fatalf("cols %d", cols)
	}

	rows, err := term.Rows()
	if err != nil {
		t.Fatal(err)
	}
	if rows != 24 {
		t.Fatalf("rows %d", rows)
	}

	cursorX, err := term.CursorX()
	if err != nil {
		t.Fatal(err)
	}
	if cursorX != 5 {
		t.Fatalf("cursorX %d", cursorX)
	}

	cursorY, err := term.CursorY()
	if err != nil {
		t.Fatal(err)
	}
	if cursorY != 1 {
		t.Fatalf("cursorY %d", cursorY)
	}

	active, err := term.ActiveScreen()
	if err != nil {
		t.Fatal(err)
	}
	if active != libghostty.ScreenPrimary {
		t.Fatalf("active screen %d", active)
	}

	ground, err := term.VTGround()
	if err != nil {
		t.Fatal(err)
	}
	if ground != true {
		t.Fatal("not on ground")
	}

	if err := term.Resize(120, 40, 8, 16); err != nil {
		t.Fatal(err)
	}

	cols, err = term.Cols()
	if err != nil {
		t.Fatal(err)
	}
	if cols != 120 {
		t.Fatalf("cols %d", cols)
	}

	rows, err = term.Rows()
	if err != nil {
		t.Fatal(err)
	}
	if rows != 40 {
		t.Fatalf("rows %d", rows)
	}

	term.Reset()

	formatted = formatTerminal(
		t,
		term,
		libghostty.WithFormatterFormat(libghostty.FormatterFormatPlain),
		libghostty.WithFormatterTrim(true),
	)
	if len(formatted) != 0 {
		t.Fatalf("formatted after reset %q", formatted)
	}
}

func TestTerminalRejectsInvalidSize(t *testing.T) {
	term, err := libghostty.NewTerminal(libghostty.WithSize(0, 24))
	if term != nil {
		t.Fatal("terminal created")
	}

	ghosttyErr, ok := errors.AsType[*libghostty.Error](err)
	if !ok {
		t.Fatalf("error %v", err)
	}
	if ghosttyErr.Result != libghostty.ResultInvalidValue {
		t.Fatalf("result %d", ghosttyErr.Result)
	}
}

func TestTerminalRejectsOversizedWASMValue(t *testing.T) {
	if uint64(^uint(0)) == uint64(^uint32(0)) {
		t.Skip("uint is 32 bits")
	}

	term := newTerminal(t, 80, 24)
	value := uint64(1) << 32

	err := term.SetContinuationMaxBytes(uint(value))
	ghosttyErr, ok := errors.AsType[*libghostty.Error](err)
	if !ok {
		t.Fatalf("error %v", err)
	}
	if ghosttyErr.Result != libghostty.ResultLimitExceeded {
		t.Fatalf("result %d", ghosttyErr.Result)
	}
}

func TestTerminalPwd(t *testing.T) {
	term := newTerminal(t, 80, 24)
	term.VTWrite([]byte("\x1b]7;file://localhost/tmp/example\x07"))

	pwd, err := term.Pwd()
	if err != nil {
		t.Fatal(err)
	}
	if pwd != "file://localhost/tmp/example" {
		t.Fatalf("pwd %q", pwd)
	}
}

func TestFormatterSelection(t *testing.T) {
	term := newTerminal(t, 5, 2)
	term.VTWrite([]byte("one\r\ntwo"))

	start, err := term.GridRef(libghostty.Point{Tag: libghostty.PointTagActive, X: 0, Y: 1})
	if err != nil {
		t.Fatal(err)
	}

	end, err := term.GridRef(libghostty.Point{Tag: libghostty.PointTagActive, X: 4, Y: 1})
	if err != nil {
		t.Fatal(err)
	}

	formatted := formatTerminal(
		t,
		term,
		libghostty.WithFormatterFormat(libghostty.FormatterFormatPlain),
		libghostty.WithFormatterTrim(true),
		libghostty.WithFormatterSelection(&libghostty.Selection{Start: *start, End: *end}),
	)
	if string(formatted) != "two" {
		t.Fatalf("formatted %q", formatted)
	}
}

func TestFormatterBuf(t *testing.T) {
	term := newTerminal(t, 80, 24)
	term.VTWrite([]byte("hello"))

	formatter, err := libghostty.NewFormatter(
		term,
		libghostty.WithFormatterTrim(true),
		libghostty.WithFormatterExtraTabstops(false),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer formatter.Close()

	required, err := formatter.FormatBuf(nil)
	ghosttyErr, ok := errors.AsType[*libghostty.Error](err)
	if !ok {
		t.Fatalf("error %v", err)
	}
	if ghosttyErr.Result != libghostty.ResultOutOfSpace {
		t.Fatalf("result %d", ghosttyErr.Result)
	}
	if required != 5 {
		t.Fatalf("required %d", required)
	}

	buf := make([]byte, required)
	written, err := formatter.FormatBuf(buf)
	if err != nil {
		t.Fatal(err)
	}
	if written != 5 {
		t.Fatalf("written %d", written)
	}
	if string(buf) != "hello" {
		t.Fatalf("buf %q", buf)
	}

	formatted, err := formatter.FormatString()
	if err != nil {
		t.Fatal(err)
	}
	if formatted != "hello" {
		t.Fatalf("formatted %q", formatted)
	}
}

func TestKeyEncoder(t *testing.T) {
	term := newTerminal(t, 80, 24)

	encoder, err := libghostty.NewKeyEncoder()
	if err != nil {
		t.Fatal(err)
	}
	defer encoder.Close()

	event, err := libghostty.NewKeyEvent()
	if err != nil {
		t.Fatal(err)
	}
	defer event.Close()

	encoder.SetOptBool(libghostty.KeyEncoderOptCursorKeyApplication, false)
	encoder.SetOptKittyFlags(libghostty.KittyKeyDisabled)
	encoder.SetOptOptionAsAlt(libghostty.OptionAsAltFalse)

	event.SetAction(libghostty.KeyActionPress)
	event.SetKey(libghostty.KeyC)
	event.SetMods(libghostty.ModCtrl)
	event.SetUnshiftedCodepoint('c')

	if event.Action() != libghostty.KeyActionPress {
		t.Fatal("action")
	}
	if event.Key() != libghostty.KeyC {
		t.Fatal("key")
	}
	if event.Mods() != libghostty.ModCtrl {
		t.Fatal("mods")
	}
	if event.ConsumedMods() != libghostty.Mods(0) {
		t.Fatal("consumed mods")
	}
	if event.Composing() {
		t.Fatal("composing")
	}
	if event.UnshiftedCodepoint() != 'c' {
		t.Fatal("codepoint")
	}
	if event.UTF8() != "" {
		t.Fatal("utf8")
	}

	encoder.SetOptFromTerminal(term)
	data, err := encoder.Encode(event)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "\x03" {
		t.Fatalf("encoded %q", data)
	}

	term.VTWrite([]byte("\x1b[>1u"))
	event.SetKey(libghostty.KeyArrowUp)
	event.SetMods(libghostty.ModCtrl | libghostty.ModShift)
	event.SetUnshiftedCodepoint(0)
	encoder.SetOptFromTerminal(term)

	data, err = encoder.Encode(event)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "\x1b[1;6A" {
		t.Fatalf("encoded %q", data)
	}
}

func TestSnapshotRetainsUnfinishedContinuation(t *testing.T) {
	term := newTerminal(t, 80, 24)
	term.VTWrite([]byte("before\x1b[31"))

	ground, err := term.VTGround()
	if err != nil {
		t.Fatal(err)
	}
	if ground {
		t.Fatal("on ground")
	}

	snapshot, err := term.Snapshot()
	if err != nil {
		t.Fatal(err)
	}

	decoder, err := libghostty.NewSnapshotDecoderBytes(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	defer decoder.Close()

	if err := decoder.SetMaxContinuationBytes(65 << 20); err != nil {
		t.Fatal(err)
	}
	if err := decoder.SetRetainContinuation(true); err != nil {
		t.Fatal(err)
	}

	restored, err := decoder.Decode()
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()

	resnapshot, err := restored.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if string(resnapshot) != string(snapshot) {
		t.Fatal("snapshot round trip changed")
	}

	copyDecoder, err := libghostty.NewSnapshotDecoderBytesCopy(snapshot)
	if err != nil {
		t.Fatal(err)
	}

	copyRestored, err := copyDecoder.Decode()
	if err != nil {
		t.Fatal(err)
	}

	copyRestored.Close()
	copyDecoder.Close()

	restored.VTWrite([]byte("mred"))

	formatted := formatTerminal(
		t,
		restored,
		libghostty.WithFormatterFormat(libghostty.FormatterFormatPlain),
		libghostty.WithFormatterTrim(true),
	)
	if string(formatted) != "beforered" {
		t.Fatalf("formatted %q", formatted)
	}
}

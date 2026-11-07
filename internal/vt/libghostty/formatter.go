//go:build goexperiment.simd

package libghostty

import (
	"bytes"
	"encoding/binary"
)

const (
	formatterOptEmit      = 1
	formatterOptUnwrap    = 2
	formatterOptTrim      = 3
	formatterOptSelection = 4
	formatterOptExtras    = 5

	selectionSize = 20
)

type FormatterFormat int

const (
	FormatterFormatPlain FormatterFormat = 0
	FormatterFormatVT    FormatterFormat = 1
	FormatterFormatHTML  FormatterFormat = 2
)

type formatterOpts struct {
	format             FormatterFormat
	unwrap             bool
	trim               bool
	selection          *Selection
	extraPalette       bool
	extraModes         bool
	extraScrolling     bool
	extraTabstops      bool
	extraPwd           bool
	extraKeyboard      bool
	extraCursor        bool
	extraStyle         bool
	extraHyperlink     bool
	extraProtection    bool
	extraKittyKeyboard bool
	extraCharsets      bool
}

type FormatterOption func(*formatterOpts)

func WithFormatterFormat(format FormatterFormat) FormatterOption {
	return func(opts *formatterOpts) { opts.format = format }
}

func WithFormatterUnwrap(unwrap bool) FormatterOption {
	return func(opts *formatterOpts) { opts.unwrap = unwrap }
}

func WithFormatterTrim(trim bool) FormatterOption {
	return func(opts *formatterOpts) { opts.trim = trim }
}

func WithFormatterSelection(selection *Selection) FormatterOption {
	return func(opts *formatterOpts) { opts.selection = selection }
}

func WithFormatterExtraPalette(value bool) FormatterOption {
	return func(opts *formatterOpts) { opts.extraPalette = value }
}

func WithFormatterExtraModes(value bool) FormatterOption {
	return func(opts *formatterOpts) { opts.extraModes = value }
}

func WithFormatterExtraScrollingRegion(value bool) FormatterOption {
	return func(opts *formatterOpts) { opts.extraScrolling = value }
}

func WithFormatterExtraTabstops(value bool) FormatterOption {
	return func(opts *formatterOpts) { opts.extraTabstops = value }
}

func WithFormatterExtraPwd(value bool) FormatterOption {
	return func(opts *formatterOpts) { opts.extraPwd = value }
}

func WithFormatterExtraKeyboard(value bool) FormatterOption {
	return func(opts *formatterOpts) { opts.extraKeyboard = value }
}

func WithFormatterExtraCursor(value bool) FormatterOption {
	return func(opts *formatterOpts) { opts.extraCursor = value }
}

func WithFormatterExtraStyle(value bool) FormatterOption {
	return func(opts *formatterOpts) { opts.extraStyle = value }
}

func WithFormatterExtraHyperlink(value bool) FormatterOption {
	return func(opts *formatterOpts) { opts.extraHyperlink = value }
}

func WithFormatterExtraProtection(value bool) FormatterOption {
	return func(opts *formatterOpts) { opts.extraProtection = value }
}

func WithFormatterExtraKittyKeyboard(value bool) FormatterOption {
	return func(opts *formatterOpts) { opts.extraKittyKeyboard = value }
}

func WithFormatterExtraCharsets(value bool) FormatterOption {
	return func(opts *formatterOpts) { opts.extraCharsets = value }
}

type Formatter struct {
	rt  *wasmRuntime
	ptr uint32
}

func NewFormatter(t *Terminal, options ...FormatterOption) (*Formatter, error) {
	opts := formatterOpts{}
	for _, option := range options {
		option(&opts)
	}

	t.rt.mu.Lock()
	defer t.rt.mu.Unlock()

	ptr, err := t.rt.opaque(func(slot uint32) int32 {
		return t.rt.mod.Xghostty_formatter_terminal_new(0, int32(slot), int32(t.ptr))
	})
	if err != nil {
		return nil, err
	}

	f := &Formatter{rt: t.rt, ptr: ptr}

	fail := func(err error) (*Formatter, error) {
		t.rt.mod.Xghostty_formatter_free(int32(ptr))
		return nil, err
	}

	if err := f.setUint32Locked(formatterOptEmit, uint32(opts.format)); err != nil {
		return fail(err)
	}

	if err := f.setBoolLocked(formatterOptUnwrap, opts.unwrap); err != nil {
		return fail(err)
	}

	if err := f.setBoolLocked(formatterOptTrim, opts.trim); err != nil {
		return fail(err)
	}

	if err := f.setUint32Locked(formatterOptExtras, extrasMask(&opts)); err != nil {
		return fail(err)
	}

	if opts.selection != nil {
		if err := f.setSelectionLocked(opts.selection); err != nil {
			return fail(err)
		}
	}

	return f, nil
}

func extrasMask(opts *formatterOpts) uint32 {
	var extras uint32
	if opts.extraPalette {
		extras |= 1 << 0
	}
	if opts.extraModes {
		extras |= 1 << 1
	}
	if opts.extraScrolling {
		extras |= 1 << 2
	}
	if opts.extraTabstops {
		extras |= 1 << 3
	}
	if opts.extraPwd {
		extras |= 1 << 4
	}
	if opts.extraKeyboard {
		extras |= 1 << 5
	}
	if opts.extraCursor {
		extras |= 1 << 6
	}
	if opts.extraStyle {
		extras |= 1 << 7
	}
	if opts.extraHyperlink {
		extras |= 1 << 8
	}
	if opts.extraProtection {
		extras |= 1 << 9
	}
	if opts.extraKittyKeyboard {
		extras |= 1 << 10
	}
	if opts.extraCharsets {
		extras |= 1 << 11
	}

	return extras
}

func (f *Formatter) setUint32Locked(option int32, value uint32) error {
	return f.rt.value32(value, func(ptr uint32) int32 {
		return f.rt.mod.Xghostty_formatter_set(int32(f.ptr), option, int32(ptr))
	})
}

func (f *Formatter) setBoolLocked(option int32, value bool) error {
	ptr, err := f.rt.alloc(1)
	if err != nil {
		return err
	}
	defer f.rt.free(ptr, 1)

	data, err := f.rt.bytes(ptr, 1)
	if err != nil {
		return err
	}

	data[0] = boolByte(value)

	return resultError(f.rt.mod.Xghostty_formatter_set(int32(f.ptr), option, int32(ptr)))
}

func (f *Formatter) setSelectionLocked(selection *Selection) error {
	ptr, err := f.rt.alloc(selectionSize)
	if err != nil {
		return err
	}
	defer f.rt.free(ptr, selectionSize)

	data, err := f.rt.bytes(ptr, selectionSize)
	if err != nil {
		return err
	}

	clear(data)
	copy(data[0:], selection.Start.data[:])
	copy(data[8:], selection.End.data[:])
	data[16] = boolByte(selection.Rectangle)

	return resultError(f.rt.mod.Xghostty_formatter_set(int32(f.ptr), formatterOptSelection, int32(ptr)))
}

func (f *Formatter) Close() {
	if f == nil || f.ptr == 0 {
		return
	}

	f.rt.mu.Lock()
	defer f.rt.mu.Unlock()

	f.rt.mod.Xghostty_formatter_free(int32(f.ptr))
	f.ptr = 0
}

func (f *Formatter) Format() ([]byte, error) {
	f.rt.mu.Lock()
	defer f.rt.mu.Unlock()

	outPtrSlot, err := f.rt.alloc(8)
	if err != nil {
		return nil, err
	}
	defer f.rt.free(outPtrSlot, 8)

	result := f.rt.mod.Xghostty_formatter_format_alloc(
		int32(f.ptr),
		0,
		int32(outPtrSlot),
		int32(outPtrSlot+4),
	)
	if err := resultError(result); err != nil {
		return nil, err
	}

	output, err := f.rt.bytes(outPtrSlot, 8)
	if err != nil {
		return nil, err
	}

	ptr := binary.LittleEndian.Uint32(output)
	length := binary.LittleEndian.Uint32(output[4:])
	defer f.rt.mod.Xghostty_free(0, int32(ptr), int32(length))

	data, err := f.rt.bytes(ptr, length)
	if err != nil {
		return nil, err
	}

	return bytes.Clone(data), nil
}

func (f *Formatter) FormatBuf(buf []byte) (int, error) {
	length, err := wasmLength(len(buf))
	if err != nil {
		return 0, err
	}

	f.rt.mu.Lock()
	defer f.rt.mu.Unlock()

	var ptr uint32
	if len(buf) > 0 {
		ptr, err = f.rt.alloc(length)
		if err != nil {
			return 0, err
		}
		defer f.rt.free(ptr, length)
	}

	writtenPtr, err := f.rt.alloc(4)
	if err != nil {
		return 0, err
	}
	defer f.rt.free(writtenPtr, 4)

	result := f.rt.mod.Xghostty_formatter_format_buf(
		int32(f.ptr),
		int32(ptr),
		int32(length),
		int32(writtenPtr),
	)
	writtenData, err := f.rt.bytes(writtenPtr, 4)
	if err != nil {
		return 0, err
	}

	written := binary.LittleEndian.Uint32(writtenData)
	if result == int32(ResultSuccess) && written > 0 {
		data, err := f.rt.bytes(ptr, written)
		if err != nil {
			return 0, err
		}

		copy(buf, data)
	}

	return int(written), resultError(result)
}

func (f *Formatter) FormatString() (string, error) {
	data, err := f.Format()

	return string(data), err
}

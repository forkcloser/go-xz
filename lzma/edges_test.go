// Copyright 2026 Forkcloser. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lzma_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"testing"

	"github.com/forkcloser/go-xz/v2/internal/lzmaspec"
	"github.com/forkcloser/go-xz/v2/lzma"
)

// TestEdgeShortCompressedChunk: a compressed LZMA2 chunk whose size field
// leaves fewer than the range coder's five bytes is corrupt, not the end of
// the sequence.
func TestEdgeShortCompressedChunk(t *testing.T) {
	t.Parallel()

	for n := 1; n <= 5; n++ {
		// cLRND, one uncompressed byte, compressed size n, properties 0x5D.
		in := append([]byte{0xE0, 0x00, 0x00, 0x00, byte(n - 1), 0x5D}, make([]byte, n)...)

		r, err := lzma.NewReader2(bytes.NewReader(in), 8<<20)
		if err != nil {
			t.Fatalf("n=%d: NewReader2: %v", n, err)
		}

		b, err := io.ReadAll(r)
		if len(b) != 0 || !errors.Is(err, lzma.ErrCorrupt) {
			t.Errorf("n=%d: %d bytes, %v; want 0 bytes and ErrCorrupt", n, len(b), err)
		}
	}

	// The same after a chunk that decoded, whatever the short one resets:
	// it is not skipped, and the sequence does not end well without it.
	data := bytes.Repeat([]byte("abc"), 100)

	decoded := lzma2Stream(t, data)
	if decoded[0] != 0xE0 {
		t.Fatalf("the writer's first chunk is %#x, not a compressed one", decoded[0])
	}

	decoded = decoded[:len(decoded)-1]

	for _, control := range []byte{0x80, 0xA0, 0xC0, 0xE0} {
		for n := 1; n < 5; n++ {
			// One uncompressed byte, compressed size n, properties 0x5D where
			// the control byte announces them.
			in := append(bytes.Clone(decoded), control, 0x00, 0x00, 0x00, byte(n-1))

			if control >= 0xC0 {
				in = append(in, 0x5D)
			}

			in = append(in, make([]byte, n)...)
			in = append(in, 0x00)

			r, err := lzma.NewReader2(bytes.NewReader(in), 8<<20)
			if err != nil {
				t.Fatalf("control %#x, n=%d: NewReader2: %v", control, n, err)
			}

			b, err := io.ReadAll(r)
			if !errors.Is(err, lzma.ErrCorrupt) || !bytes.HasPrefix(data, b) {
				t.Errorf("control %#x, n=%d after a chunk: %d bytes, %v; want ErrCorrupt", control, n, len(b), err)
			}

			if _, refErr := lzmaspec.DecodeLZMA2(in, 8<<20); refErr == nil {
				t.Errorf("control %#x, n=%d after a chunk: the reference decodes it", control, n)
			}
		}
	}
}

// TestEdgeHeaderOnlyInput: a classic stream cut inside the range coder's
// first five bytes is a stream cut short. Five bytes that no encoder writes,
// a first one that is not zero or a code as large as the range it starts
// with, are ErrCorrupt, "a range coder state no encoder produces", and not
// a stream cut short for having nothing after them.
func TestEdgeHeaderOnlyInput(t *testing.T) {
	t.Parallel()

	stream := classicStream(t, []byte("hello"))

	for n := lzma.HeaderLen; n < lzma.HeaderLen+5; n++ {
		_, err := lzma.NewReader(bytes.NewReader(stream[:n]))
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Errorf("%d bytes: %v; want io.ErrUnexpectedEOF", n, err)
		}
	}

	if _, err := lzma.NewReader(bytes.NewReader(stream[:lzma.HeaderLen+5])); err != nil {
		t.Errorf("header and five bytes: %v; want a reader", err)
	}

	for _, five := range [][]byte{{1, 0, 0, 0, 0}, {0, 0xff, 0xff, 0xff, 0xff}} {
		in := append(bytes.Clone(stream[:lzma.HeaderLen]), five...)

		r, err := lzma.NewReader(bytes.NewReader(in))
		if err == nil {
			_, err = io.ReadAll(r)
		}

		if !errors.Is(err, lzma.ErrCorrupt) {
			t.Errorf("header and % x: %v; want ErrCorrupt", five, err)
		}
	}
}

// TestEdgeReaderErrorIsFinal: the data decoded before a failure comes with
// the error, and every later Read returns the error again with no data.
func TestEdgeReaderErrorIsFinal(t *testing.T) {
	t.Parallel()

	data := bytes.Repeat([]byte("abc"), 100)
	stream := classicStream(t, data)

	r, err := lzma.NewReader(bytes.NewReader(stream[:len(stream)-3]))
	if err != nil {
		t.Fatal(err)
	}

	p := make([]byte, 1<<20)

	n, err := r.Read(p)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("first Read: %d bytes, %v; want io.ErrUnexpectedEOF", n, err)
	}

	if !bytes.Equal(p[:n], data[:n]) {
		t.Fatal("the bytes delivered with the error are not the stream's prefix")
	}

	for i := range 2 {
		if n, err := r.Read(p); n != 0 || !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("Read %d after the error: %d bytes, %v; want 0 and the same error", i+2, n, err)
		}
	}
}

// failAfter serves n bytes of data, then fails with err.
type failAfter struct {
	data []byte
	n    int
	err  error
}

func (f *failAfter) Read(p []byte) (int, error) {
	if f.n == 0 {
		return 0, f.err
	}

	k := copy(p[:min(len(p), f.n)], f.data)
	f.data, f.n = f.data[k:], f.n-k

	return k, nil
}

// TestEdgeSourceErrors: an error of the source comes back as it is and
// matches neither sentinel; an end of input inside a stream, io.EOF or an
// error wrapping it, is io.ErrUnexpectedEOF; and either reader returns what
// it failed with on every later Read.
func TestEdgeSourceErrors(t *testing.T) {
	t.Parallel()

	data := bytes.Repeat([]byte("the quick brown fox "), 400)
	errSource := errors.New("source failed")

	readers := map[string]struct {
		stream []byte
		open   func(io.Reader) (io.Reader, error)
	}{
		"classic": {classicStream(t, data), func(in io.Reader) (io.Reader, error) { return lzma.NewReader(in) }},
		"lzma2":   {lzma2Stream(t, data), func(in io.Reader) (io.Reader, error) { return lzma.NewReader2(in, 8<<20) }},
	}

	// asItIs is a cause the reader is to return itself, not only its kind.
	causes := map[string]struct {
		cause, want error
		asItIs      bool
	}{
		"an error":            {errSource, errSource, true},
		"a wrapped error":     {fmt.Errorf("read: %w", errSource), errSource, true},
		"io.EOF":              {io.EOF, io.ErrUnexpectedEOF, false},
		"wrapping io.EOF":     {fmt.Errorf("gone: %w", io.EOF), io.ErrUnexpectedEOF, false},
		"io.ErrUnexpectedEOF": {io.ErrUnexpectedEOF, io.ErrUnexpectedEOF, true},
	}

	for name, rd := range readers {
		for what, c := range causes {
			r, err := rd.open(&failAfter{data: rd.stream, n: len(rd.stream) / 2, err: c.cause})
			if err != nil {
				t.Fatalf("%s, %s: opening: %v", name, what, err)
			}

			_, err = io.ReadAll(r)
			if !errors.Is(err, c.want) || errors.Is(err, lzma.ErrCorrupt) || errors.Is(err, lzma.ErrUnsupported) {
				t.Errorf("%s, %s: %v; want %v and neither sentinel", name, what, err, c.want)
			}

			if c.asItIs && !errors.Is(err, c.cause) {
				t.Errorf("%s, %s: %v; want the source's own error", name, what, err)
			}

			for i := range 2 {
				if n, again := r.Read(make([]byte, 16)); n != 0 || !errors.Is(again, c.want) {
					t.Errorf("%s, %s: Read %d after the error: %d bytes, %v; want 0 and the same error",
						name, what, i+2, n, again)
				}
			}
		}
	}
}

// TestEdgeEndOfInputWrapped: input that ends "with io.EOF or an error
// wrapping it" says the same of a stream wherever it ends: in the header,
// in a chunk header, in the data, or at the end of a sized stream that
// carries a marker. Each stream is cut at every byte and read twice, from a
// source ending with io.EOF and from one ending with an error that wraps
// it; both readings end the same way.
func TestEdgeEndOfInputWrapped(t *testing.T) {
	t.Parallel()

	text := []byte("the quick brown fox jumps over the lazy dog")

	// Bytes no encoder shortens, so the LZMA2 writer stores them.
	rng := rand.New(rand.NewPCG(3, 5))
	random := make([]byte, 3000)

	for i := range random {
		random[i] = byte(rng.Uint32())
	}

	classicWith := func(opts ...lzma.Option) []byte {
		return classic(t, wcase{opts: opts}, text)
	}

	open2 := func(in io.Reader) (io.Reader, error) { return lzma.NewReader2(in, 8<<20) }
	open1 := func(in io.Reader) (io.Reader, error) { return lzma.NewReader(in) }

	// kind is how a reading ended: whole, cut short, corrupt, or otherwise.
	kind := func(open func(io.Reader) (io.Reader, error), stream []byte, n int, end error) string {
		r, err := open(&failAfter{data: stream, n: n, err: end})
		if err == nil {
			_, err = io.ReadAll(r)
		}

		switch {
		case err == nil:
			return "whole"
		case errors.Is(err, io.ErrUnexpectedEOF):
			return "cut short"
		case errors.Is(err, lzma.ErrCorrupt):
			return "corrupt"
		default:
			return err.Error()
		}
	}

	wrapped := fmt.Errorf("gone: %w", io.EOF)

	for name, tc := range map[string]struct {
		stream []byte
		open   func(io.Reader) (io.Reader, error)
	}{
		"classic, a marker":            {classicWith(), open1},
		"classic, a size":              {classicWith(lzma.WithSize(int64(len(text)))), open1},
		"classic, a size and a marker": {classicWith(lzma.WithSize(int64(len(text))), lzma.WithEOSMarker()), open1},
		"lzma2, compressed chunks":     {lzma2Stream(t, bytes.Repeat(text, 4)), open2},
		"lzma2, uncompressed chunks":   {lzma2Stream(t, random), open2},
	} {
		for n := range len(tc.stream) {
			plain, wrap := kind(tc.open, tc.stream, n, io.EOF), kind(tc.open, tc.stream, n, wrapped)
			if plain != wrap {
				t.Errorf("%s, cut at %d of %d: %q with io.EOF, %q with an error wrapping it",
					name, n, len(tc.stream), plain, wrap)
			}
		}
	}
}

// TestEdgeCallerErrorsMatchNoSentinel: what a caller's own mistake earns, an
// option out of the format's range or a write after Close, is an error that
// matches neither ErrCorrupt nor ErrUnsupported; properties the format
// allows and the package does not are ErrUnsupported in an option too.
func TestEdgeCallerErrorsMatchNoSentinel(t *testing.T) {
	t.Parallel()

	closed := func(w io.WriteCloser, err error) error {
		if err != nil {
			t.Fatal(err)
		}

		if err = w.Close(); err != nil {
			t.Fatal(err)
		}

		_, err = w.Write([]byte("x"))

		return err
	}

	_, errDict := lzma.NewWriter(io.Discard, lzma.WithDictSize(lzma.MinDictSize-1))
	_, errDict2 := lzma.NewWriter2(io.Discard, lzma.WithDictSize(lzma.MinDictSize-1))
	_, errProps := lzma.NewWriter(io.Discard, lzma.WithProperties(9, 0, 0))
	_, errSum := lzma.NewWriter(io.Discard, lzma.WithProperties(3, 2, 0))
	_, errSize := lzma.NewWriter(io.Discard, lzma.WithSize(-1))
	_, errLimit := lzma.NewReader(bytes.NewReader(nil), lzma.WithMaxDictSize(lzma.MinDictSize-1))
	_, errReader2 := lzma.NewReader2(bytes.NewReader(nil), lzma.MinDictSize-1)

	for name, err := range map[string]error{
		"WithDictSize below the minimum, Writer":   errDict,
		"WithDictSize below the minimum, Writer2":  errDict2,
		"WithProperties, lc out of range":          errProps,
		"WithSize, negative":                       errSize,
		"WithMaxDictSize below the minimum":        errLimit,
		"NewReader2, dictionary below the minimum": errReader2,
		"Write after Close, Writer":                closed(lzma.NewWriter(io.Discard)),
		"Write after Close, Writer2":               closed(lzma.NewWriter2(io.Discard)),
	} {
		if err == nil || errors.Is(err, lzma.ErrCorrupt) || errors.Is(err, lzma.ErrUnsupported) {
			t.Errorf("%s: %v; want an error that matches neither sentinel", name, err)
		}
	}

	if !errors.Is(errSum, lzma.ErrUnsupported) {
		t.Errorf("WithProperties, lc+lp above four: %v; want ErrUnsupported", errSum)
	}
}

// TestEdgeReader2ResetForgets: Reset forgets the error the reader held and
// the end of the sequence it read: the next sequence reads as on a new
// reader, and EOS reports false until its own end.
func TestEdgeReader2ResetForgets(t *testing.T) {
	t.Parallel()

	data := bytes.Repeat([]byte("B"), 10)
	sequence := lzma2Stream(t, data)

	r, err := lzma.NewReader2(bytes.NewReader(sequence[:len(sequence)-2]), 8<<20)
	if err != nil {
		t.Fatal(err)
	}

	if _, err = io.ReadAll(r); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("cut sequence: %v; want io.ErrUnexpectedEOF", err)
	}

	for range 2 {
		r.Reset(bytes.NewReader(sequence))

		if r.EOS() {
			t.Fatal("EOS reports true before the sequence is read")
		}

		got, err := io.ReadAll(r)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("after Reset: %q, %v; want the sequence and no error", got, err)
		}

		if !r.EOS() {
			t.Fatal("EOS reports false at the end of the sequence")
		}
	}
}

// TestEdgeReader2ResetDropsUnread: Reset starts the new sequence with the
// new sequence's bytes, not what the previous one decoded and nobody read.
func TestEdgeReader2ResetDropsUnread(t *testing.T) {
	t.Parallel()

	first := lzma2Stream(t, bytes.Repeat([]byte("A"), 1000))
	second := lzma2Stream(t, bytes.Repeat([]byte("B"), 10))

	r, err := lzma.NewReader2(bytes.NewReader(first), 8<<20)
	if err != nil {
		t.Fatal(err)
	}

	if _, err = io.ReadFull(r, make([]byte, 10)); err != nil {
		t.Fatal(err)
	}

	r.Reset(bytes.NewReader(second))

	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(got, bytes.Repeat([]byte("B"), 10)) {
		t.Fatalf("after Reset: %d bytes starting %q; want the ten B", len(got), got[:min(20, len(got))])
	}
}

// TestEdgeSizeZero: an explicit uncompressed size of zero is a size, written
// as such and read back as an empty stream; so is the hand-written stream
// liblzma and xz accept.
func TestEdgeSizeZero(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	w, err := lzma.NewWriter(&buf, lzma.WithSize(0))
	if err != nil {
		t.Fatal(err)
	}

	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	if size := buf.Bytes()[5:13]; !bytes.Equal(size, make([]byte, 8)) {
		t.Fatalf("header size field % x; want zero", size)
	}

	// properties 0x5D, dictionary 4096, size 0, the range coder's flush.
	crafted := append([]byte{0x5D, 0x00, 0x10, 0x00, 0x00}, make([]byte, 13)...)

	for name, stream := range map[string][]byte{"written": buf.Bytes(), "crafted": crafted} {
		r, err := lzma.NewReader(bytes.NewReader(stream))
		if err != nil {
			t.Fatalf("%s: NewReader: %v", name, err)
		}

		if b, err := io.ReadAll(r); err != nil || len(b) != 0 {
			t.Errorf("%s: %d bytes, %v; want none and no error", name, len(b), err)
		}
	}
}

// TestEdgeWriterClosed: a second Close writes nothing and a Write after
// Close is refused.
func TestEdgeWriterClosed(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	w, err := lzma.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := w.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}

	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	size := buf.Len()

	if err := w.Close(); err == nil {
		t.Error("second Close returned nil")
	}

	if buf.Len() != size {
		t.Errorf("second Close wrote %d bytes", buf.Len()-size)
	}

	if _, err := w.Write([]byte("y")); err == nil {
		t.Error("Write after Close returned nil")
	}
}

// TestEdgeShortSizeIsNotCorruption: closing before the declared size is the
// caller's error, not a corrupt stream.
func TestEdgeShortSizeIsNotCorruption(t *testing.T) {
	t.Parallel()

	w, err := lzma.NewWriter(io.Discard, lzma.WithSize(10))
	if err != nil {
		t.Fatal(err)
	}

	if err := w.Close(); err == nil || errors.Is(err, lzma.ErrCorrupt) {
		t.Errorf("Close short of the size: %v; want an error that is not ErrCorrupt", err)
	}
}

// TestEdgeSizeExceededIsNotAFailure: a Write refused for passing the
// declared size took what fitted, and Close finishes the stream with it.
func TestEdgeSizeExceededIsNotAFailure(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	w, err := lzma.NewWriter(&buf, lzma.WithSize(3))
	if err != nil {
		t.Fatal(err)
	}

	n, err := w.Write([]byte("abcdef"))
	if n != 3 || !errors.Is(err, lzma.ErrSizeExceeded) {
		t.Fatalf("Write past the size: %d bytes, %v; want 3 and ErrSizeExceeded", n, err)
	}

	if err = w.Close(); err != nil {
		t.Fatalf("Close: %v; want the stream finished", err)
	}

	r, err := lzma.NewReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}

	got, err := io.ReadAll(r)
	if err != nil || string(got) != "abc" {
		t.Errorf("read back %q, %v; want abc", got, err)
	}
}

// classicStream is data in the classic format with the default writer.
func classicStream(tb testing.TB, data []byte) []byte {
	tb.Helper()

	var buf bytes.Buffer

	w, err := lzma.NewWriter(&buf)
	if err != nil {
		tb.Fatal(err)
	}

	if _, err := w.Write(data); err != nil {
		tb.Fatal(err)
	}

	if err := w.Close(); err != nil {
		tb.Fatal(err)
	}

	return buf.Bytes()
}

// TestEdgeMarkerAfterSizeEndsTheCoder: a stream with both a declared size
// and an end marker must end its range coder with the marker; a byte
// corrupted after the marker is corrupt, as it is after a marker alone.
func TestEdgeMarkerAfterSizeEndsTheCoder(t *testing.T) {
	t.Parallel()

	data := bytes.Repeat([]byte("the quick brown fox "), 50)

	var buf bytes.Buffer

	w, err := lzma.NewWriter(&buf, lzma.WithSize(int64(len(data))), lzma.WithEOSMarker())
	if err != nil {
		t.Fatal(err)
	}

	if _, err = w.Write(data); err != nil {
		t.Fatal(err)
	}

	if err = w.Close(); err != nil {
		t.Fatal(err)
	}

	stream := buf.Bytes()
	stream[len(stream)-2] ^= 0x01

	r, err := lzma.NewReader(bytes.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}

	if got, err := io.ReadAll(r); !errors.Is(err, lzma.ErrCorrupt) {
		t.Fatalf("%d bytes, %v; want ErrCorrupt", len(got), err)
	}
}

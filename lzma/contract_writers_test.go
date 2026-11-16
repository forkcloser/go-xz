// Copyright 2026 Forkcloser. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// What the writers owe the caller when the writer under them fails, when
// the caller writes after closing, past the declared size, or across a
// chunk's limit.

package lzma_test

import (
	"bytes"
	"errors"
	"io"
	"math/rand/v2"
	"testing"

	"github.com/forkcloser/xz/v2/internal/lzmaspec"
	"github.com/forkcloser/xz/v2/lzma"
)

// errHiccup is what the failing writers of these tests fail with.
var errHiccup = errors.New("the underlying writer failed")

// hiccup is an underlying writer that fails once: its at-th call takes
// nothing and returns err, every other one takes what it is given, and
// calls counts them all. countingFailingWriter fails every call from one
// on, which hides a writer that loses an error and carries on.
type hiccup struct {
	at, calls int
	err       error
}

func (h *hiccup) Write(p []byte) (int, error) {
	h.calls++
	if h.calls == h.at {
		return 0, h.err
	}

	return len(p), nil
}

// byteHiccup is hiccup as a destination that takes single bytes as well.
// The classic writer writes to such a one with no buffer of its own in
// between, so every byte of the stream is a call that can fail.
type byteHiccup struct{ hiccup }

func (h *byteHiccup) WriteByte(c byte) error {
	_, err := h.Write([]byte{c})

	return err
}

// underlying is the writer of one failing run: where the writer under test
// writes, the error its one failure is to come back as, whether it has
// failed yet, and how many calls it has had since.
type underlying struct {
	dst   io.Writer
	want  error
	hit   func() bool
	after func() int
}

// failingAt fails its at-th call.
func failingAt(at int) underlying {
	h := &hiccup{at: at, err: errHiccup}

	return underlying{h, errHiccup, func() bool { return h.calls >= at }, func() int { return h.calls - at }}
}

// failingByteAt fails its at-th call, single bytes counted.
func failingByteAt(at int) underlying {
	h := &byteHiccup{hiccup{at: at, err: errHiccup}}

	return underlying{h, errHiccup, func() bool { return h.calls >= at }, func() int { return h.calls - at }}
}

// shortAt takes a byte less than it is given and reports no error, at its
// at-th call or, when that one is given nothing, at the first after it that
// is given something: the calls after the failure are counted from there.
func shortAt(at int) underlying {
	m := &miscounter{at: at, delta: -1}

	return underlying{m, io.ErrShortWrite, func() bool { return m.hit }, func() int { return m.calls - m.hitAt }}
}

// session holds the calls of one run to what the writers say of a failure
// under them. Until it happens a call returns what the test expects. The
// call that meets it returns it. Every call after that returns it again,
// takes nothing, and leaves the underlying writer alone.
type session struct {
	t      *testing.T
	name   string
	u      underlying
	failed bool
}

// check is given what a call returned, and the count it returns when
// nothing fails.
func (s *session) check(call string, n, want int, err error) {
	s.t.Helper()

	switch {
	case s.failed:
		if n != 0 || !errors.Is(err, s.u.want) {
			s.t.Fatalf("%s: %s after the failure returned %d, %v; want 0 and the failure", s.name, call, n, err)
		}
	case s.u.hit():
		s.failed = true

		if n < 0 || n > want || !errors.Is(err, s.u.want) {
			s.t.Fatalf("%s: %s met the failure and returned %d, %v", s.name, call, n, err)
		}
	default:
		if n != want || err != nil {
			s.t.Fatalf("%s: %s returned %d, %v; want %d and no error", s.name, call, n, err, want)
		}
	}

	if s.failed && s.u.after() != 0 {
		s.t.Fatalf("%s: %s: %d calls of the underlying writer after its failure", s.name, call, s.u.after())
	}
}

// scattered is n bytes that copy short runs from all over what came before
// them, with random bytes between: every match is at another distance, so
// a few hundred bytes take the distance coder down each of its branches.
func scattered(n int) []byte {
	rng := rand.New(rand.NewPCG(uint64(n), 5))
	out := make([]byte, 0, n+8)

	for len(out) < n {
		if len(out) < 64 || rng.IntN(4) == 0 {
			out = append(out, byte(rng.Uint32()))

			continue
		}

		from := rng.IntN(len(out) - 8)
		out = append(out, out[from:from+8]...)
	}

	return out[:n]
}

// TestContractWriterKeepsItsFirstError: Close's "After a failed Write it
// returns that error and writes nothing", held at every call the classic
// writer makes of the writer under it, the header, each write of its
// buffer and the last one, which is Close's own: the call that meets the
// failure returns it, and so does every Write and Close after, with no
// byte taken and nothing more written, although the writer under it would
// take it. A write that comes back a byte short with no error is
// io.ErrShortWrite the same way. A destination that takes single bytes is
// written to directly, and held to the same at every byte.
func TestContractWriterKeepsItsFirstError(t *testing.T) {
	t.Parallel()

	small := scattered(700)
	large := corpus(t)["random"]

	runs := map[string]struct {
		data  []byte
		under func(at int) underlying
	}{
		"a small stream, a failed write":     {small, failingAt},
		"a small stream, a short write":      {small, shortAt},
		"a small stream, a failed byte":      {small, failingByteAt},
		"three buffers full, a failed write": {large, failingAt},
		"three buffers full, a short write":  {large, shortAt},
	}

	for name, run := range runs {
		configs := map[string][]lzma.Option{
			"no size":         {},
			"size":            {lzma.WithSize(int64(len(run.data)))},
			"size and marker": {lzma.WithSize(int64(len(run.data))), lzma.WithEOSMarker()},
		}

		for cname, opts := range configs {
			opts = append(opts, lzma.WithDictSize(lzma.MinDictSize))
			cut := len(run.data) / 3

			for at := 1; ; at++ {
				s := &session{t: t, name: name + ", " + cname + ", call " + itoa(at), u: run.under(at)}

				w, err := lzma.NewWriter(s.u.dst, opts...)
				s.check("NewWriter", 0, 0, err)

				if s.failed {
					if w != nil {
						t.Fatalf("%s: NewWriter returned a writer with its error", s.name)
					}

					continue
				}

				if w == nil {
					t.Fatalf("%s: NewWriter returned no writer and no error", s.name)
				}

				for _, piece := range [][]byte{run.data[:cut], run.data[cut:]} {
					n, werr := w.Write(piece)
					s.check("Write", n, len(piece), werr)
				}

				s.check("Close", 0, 0, w.Close())

				if !s.failed {
					// The stream went out whole in fewer calls than at.
					if at < 2 {
						t.Fatalf("%s: nothing was written", s.name)
					}

					break
				}

				n, werr := w.Write([]byte("x"))
				s.check("Write", n, 1, werr)
				s.check("Close", 0, 0, w.Close())
			}
		}
	}
}

// TestContractWriterFlushesItsBufferOnClose: Close "ensures that all data
// from the buffer will be compressed and the LZMA stream will be finished",
// through a destination that is a Writer and nothing more as through one
// that takes single bytes: the stream is the same, and the reference
// decodes it.
func TestContractWriterFlushesItsBufferOnClose(t *testing.T) {
	t.Parallel()

	for name, data := range corpus(t) {
		var buf bytes.Buffer

		// The buffer without its WriteByte.
		w, err := lzma.NewWriter(struct{ io.Writer }{&buf})
		if err != nil {
			t.Fatal(err)
		}

		if _, err = w.Write(data); err != nil {
			t.Fatalf("%s: Write: %v", name, err)
		}

		if err = w.Close(); err != nil {
			t.Fatalf("%s: Close: %v", name, err)
		}

		if !bytes.Equal(buf.Bytes(), classic(t, wcase{}, data)) {
			t.Errorf("%s: another stream than the one a bytes.Buffer is given", name)
		}

		out, err := lzmaspec.Decode(buf.Bytes())
		if err != nil || !bytes.Equal(out.Data, data) || out.Read != buf.Len() {
			t.Errorf("%s: the reference: %d bytes from %d of %d, %v", name, len(out.Data), out.Read, buf.Len(), err)
		}
	}
}

// TestContractWriter2KeepsItsFirstError: Writer2.Close's "After a failed
// Write or Flush it returns that error and writes nothing", held at every
// call Writer2 makes of the writer under it, a chunk's header, a chunk and
// the end byte, for compressed chunks and stored ones: the Write, Flush or
// Close that meets the failure returns it, and so does every Write, Flush
// and Close after, a failed Close's included, with no byte taken and
// nothing more written. A call that comes back a byte short with no error
// is io.ErrShortWrite the same way.
func TestContractWriter2KeepsItsFirstError(t *testing.T) {
	t.Parallel()

	random := corpus(t)["random"]

	runs := map[string]struct {
		data  []byte
		dict  int
		under func(at int) underlying
	}{
		"compressed chunks, a failed write": {scattered(9000), lzma.MinDictSize, failingAt},
		"compressed chunks, a short write":  {scattered(9000), lzma.MinDictSize, shortAt},
		"stored chunks, a failed write":     {random, 1 << 16, failingAt},
		"stored chunks, a short write":      {random, 1 << 16, shortAt},
	}

	for name, run := range runs {
		ends := []int{0, len(run.data) / 3, len(run.data) / 2, len(run.data)}

		for at := 1; ; at++ {
			s := &session{t: t, name: name + ", call " + itoa(at), u: run.under(at)}

			w, err := lzma.NewWriter2(s.u.dst, lzma.WithDictSize(run.dict))
			if err != nil {
				t.Fatal(err)
			}

			for i, end := range ends[1:] {
				piece := run.data[ends[i]:end]

				n, werr := w.Write(piece)
				s.check("Write", n, len(piece), werr)
				s.check("Flush", 0, 0, w.Flush())
			}

			// The last piece is flushed already: this Close writes the end
			// byte and nothing else.
			s.check("Close", 0, 0, w.Close())

			if !s.failed {
				// Three chunks, a header and a body each, and the end.
				if at < 8 {
					t.Fatalf("%s: the sequence went out in %d writes", name, at-1)
				}

				break
			}

			n, werr := w.Write([]byte("x"))
			s.check("Write", n, 1, werr)
			s.check("Flush", 0, 0, w.Flush())
			s.check("Close", 0, 0, w.Close())
		}
	}
}

// TestContractClosedWriterTakesNothing: errors.go's "What a caller's own
// mistake earns matches neither sentinel: [...] a write after Close", and
// Close's "a second Close writes nothing and reports the writer closed",
// for both writers: after Close a Write takes no byte, Write, Flush and
// Close return an error that is neither sentinel, and the stream stays the
// one the reference decodes.
func TestContractClosedWriterTakesNothing(t *testing.T) {
	t.Parallel()

	data := []byte("hello, world")

	refused := func(t *testing.T, what string, n int, err error) {
		t.Helper()

		if n != 0 || err == nil || errors.Is(err, lzma.ErrCorrupt) || errors.Is(err, lzma.ErrUnsupported) {
			t.Errorf("%s on a closed writer: %d, %v; want 0 and an error that is neither sentinel", what, n, err)
		}
	}

	t.Run("Writer", func(t *testing.T) {
		t.Parallel()

		var buf bytes.Buffer

		w, err := lzma.NewWriter(&buf)
		if err != nil {
			t.Fatal(err)
		}

		if _, err = w.Write(data); err != nil {
			t.Fatal(err)
		}

		if err = w.Close(); err != nil {
			t.Fatal(err)
		}

		stream := bytes.Clone(buf.Bytes())

		n, err := w.Write(data)
		refused(t, "Write", n, err)
		refused(t, "Close", 0, w.Close())

		if !bytes.Equal(buf.Bytes(), stream) {
			t.Errorf("%d bytes written after Close", buf.Len()-len(stream))
		}

		if out, rerr := lzmaspec.Decode(stream); rerr != nil || !bytes.Equal(out.Data, data) {
			t.Errorf("the reference: %q, %v", out.Data, rerr)
		}
	})

	t.Run("Writer2", func(t *testing.T) {
		t.Parallel()

		var buf bytes.Buffer

		w, err := lzma.NewWriter2(&buf)
		if err != nil {
			t.Fatal(err)
		}

		if _, err = w.Write(data); err != nil {
			t.Fatal(err)
		}

		if err = w.Close(); err != nil {
			t.Fatal(err)
		}

		stream := bytes.Clone(buf.Bytes())

		n, err := w.Write(data)
		refused(t, "Write", n, err)
		refused(t, "Flush", 0, w.Flush())
		refused(t, "Close", 0, w.Close())

		if !bytes.Equal(buf.Bytes(), stream) {
			t.Errorf("%d bytes written after Close", buf.Len()-len(stream))
		}

		if got, rerr := lzmaspec.DecodeLZMA2(stream, defaultDict); rerr != nil || !bytes.Equal(got, data) {
			t.Errorf("the reference: %q, %v", got, rerr)
		}
	})
}

// TestContractDeclaredSizeIsTakenToTheByte: WithSize's "Write accepts
// exactly that many bytes and refuses the rest with ErrSizeExceeded", for a
// size of zero as for any other, in writes that end before the size, on it
// and across it: each Write takes what is left of the size and no more,
// with ErrSizeExceeded when that is less than it was given. Close's "A
// Write refused with ErrSizeExceeded is not a failed one": Close finishes
// the stream, and the reference decodes the size's worth of the data.
func TestContractDeclaredSizeIsTakenToTheByte(t *testing.T) {
	t.Parallel()

	data := []byte("0123456789abcdef")

	for _, size := range []int{0, 1, 3, 4, 7} {
		var buf bytes.Buffer

		w, err := lzma.NewWriter(&buf, lzma.WithSize(int64(size)))
		if err != nil {
			t.Fatal(err)
		}

		// Three bytes at a time, then nothing, then one more.
		taken := 0
		for _, piece := range [][]byte{data[0:3], data[3:6], data[6:9], nil, data[9:10]} {
			want := min(len(piece), size-taken)

			n, werr := w.Write(piece)
			refused := errors.Is(werr, lzma.ErrSizeExceeded)

			if n != want || refused != (want < len(piece)) || (werr != nil) != refused {
				t.Fatalf("size %d, %d taken, Write of %d: %d, %v; want %d", size, taken, len(piece), n, werr, want)
			}

			taken += n
		}

		if err = w.Close(); err != nil {
			t.Fatalf("size %d: Close: %v", size, err)
		}

		out, err := lzmaspec.Decode(buf.Bytes())
		if err != nil || !bytes.Equal(out.Data, data[:size]) || out.Marker {
			t.Errorf("size %d: the reference: %q, marker %v, %v", size, out.Data, out.Marker, err)
		}
	}
}

// TestContractChunkFillsAcrossWrites: Write "writes data to the LZMA2
// stream" however the data is cut. A chunk holds two mebibytes of data at
// most, and data that compresses fills it to that before anything else
// ends it; the writes here stop a byte short of it, on it and a byte past
// it, and start again from there. The reference decodes the input from
// each sequence.
func TestContractChunkFillsAcrossWrites(t *testing.T) {
	t.Parallel()

	const chunk = 1 << 21

	data := make([]byte, chunk+chunk/2)
	c := w2case{opts: []lzma.Option{lzma.WithDictSize(lzma.MinDictSize)}, dict: lzma.MinDictSize}

	for _, cut := range [][]int{{chunk / 2}, {chunk - 1}, {chunk - 1, chunk}, {chunk}, {chunk + 1}, {1, chunk + 1}} {
		var buf bytes.Buffer

		w, err := lzma.NewWriter2(&buf, c.opts...)
		if err != nil {
			t.Fatal(err)
		}

		// No Flush between the writes, as lzma2 would: the chunk in
		// progress is what the next write adds to.
		pos := 0
		for _, end := range append(cut, len(data)) {
			if n, werr := w.Write(data[pos:end]); n != end-pos || werr != nil {
				t.Fatalf("cuts %v: Write of %d at %d: %d, %v", cut, end-pos, pos, n, werr)
			}

			pos = end
		}

		if err = w.Close(); err != nil {
			t.Fatalf("cuts %v: Close: %v", cut, err)
		}

		got, err := lzmaspec.DecodeLZMA2(buf.Bytes(), uint32(c.dictSize()))
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("cuts %v: the reference: %d bytes, %v", cut, len(got), err)
		}
	}
}

// Copyright 2026 Forkcloser. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The contract tests hold the package to the LZMA specification through the
// reference decoder (internal/lzmaspec, a port of the LZMA SDK's LzmaSpec.cpp
// with liblzma's LZMA2 chunk rules), which shares no code with it:
//
//   - What the writers produce, the reference decodes to the input, with the
//     end marker and the declared size where the configuration put them.
//   - What the reference decodes, the readers decode to the same bytes; what
//     it refuses, the readers refuse, as ErrCorrupt, ErrUnsupported or
//     io.ErrUnexpectedEOF: every byte flipped and every truncation of a
//     corpus of streams.
//   - The header codecs agree with the specification over every value: all
//     225 properties codes, all 41 dictionary codes.
//   - The chunk rules: the first chunk resets the dictionary, a dictionary
//     reset demands properties, a chunk after the end is not read.

package lzma_test

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/forkcloser/xz/v2/internal/lzmaspec"
	"github.com/forkcloser/xz/v2/lzma"
)

// corpus is the inputs every contract runs over: empty, one byte, a chunk
// of text, text past a dictionary's minimum, random bytes, zeros, and a
// mix, so matches, literals, repeats and uncompressed chunks all occur.
func corpus(tb testing.TB) map[string][]byte {
	tb.Helper()

	rng := rand.New(rand.NewPCG(7, 11))
	random := make([]byte, 20000)

	for i := range random {
		random[i] = byte(rng.Uint32())
	}

	text := bytes.Repeat([]byte("the quick brown fox jumps over the lazy dog. "), 400)
	mixed := append(bytes.Clone(text[:5000]), random[:5000]...)
	mixed = append(mixed, text[:5000]...)

	return map[string][]byte{
		"empty":  {},
		"byte":   {'x'},
		"short":  []byte("hello, world"),
		"text":   text,
		"random": random,
		"zeros":  make([]byte, 10000),
		"mixed":  mixed,
	}
}

// wcase is one classic writer configuration under test: its options, the
// dictionary they select (the default when zero) and whether the stream
// carries an end marker.
type wcase struct {
	opts   []lzma.Option
	dict   int
	marker bool
}

// w2case is one LZMA2 writer configuration under test.
type w2case struct {
	opts []lzma.Option
	dict int
}

// defaultDict is the writers' dictionary when the options name none.
const defaultDict = 8 << 20

func (c w2case) dictSize() int {
	if c.dict == 0 {
		return defaultDict
	}

	return c.dict
}

// writerConfigs are the classic writer's configurations the contracts run
// with: the default, an explicit size, size and marker both, the smallest
// dictionary, and the properties at their extremes.
func writerConfigs(n int) map[string]wcase {
	return map[string]wcase{
		"default":         {marker: true},
		"size":            {opts: []lzma.Option{lzma.WithSize(int64(n))}},
		"size and marker": {opts: []lzma.Option{lzma.WithSize(int64(n)), lzma.WithEOSMarker()}, marker: true},
		"small dict": {
			opts:   []lzma.Option{lzma.WithDictSize(lzma.MinDictSize)},
			dict:   lzma.MinDictSize,
			marker: true,
		},
		"lc0 lp4":     {opts: []lzma.Option{lzma.WithProperties(0, 4, 2)}, marker: true},
		"lc4 lp0 pb0": {opts: []lzma.Option{lzma.WithProperties(4, 0, 0)}, marker: true},
		"pb4":         {opts: []lzma.Option{lzma.WithProperties(3, 0, 4)}, marker: true},
	}
}

func classic(tb testing.TB, c wcase, data []byte) []byte {
	tb.Helper()

	var buf bytes.Buffer

	w, err := lzma.NewWriter(&buf, c.opts...)
	if err != nil {
		tb.Fatal(err)
	}

	if _, err = w.Write(data); err != nil {
		tb.Fatal(err)
	}

	if err = w.Close(); err != nil {
		tb.Fatal(err)
	}

	return buf.Bytes()
}

// TestContractClassicWriter: every stream the classic writer produces is
// what the reference decodes to the input, with the end marker when the
// configuration asked for one or declared no size, and the declared size
// in the header.
func TestContractClassicWriter(t *testing.T) {
	t.Parallel()

	for name, data := range corpus(t) {
		for cname, c := range writerConfigs(len(data)) {
			stream := classic(t, c, data)

			out, err := lzmaspec.Decode(stream)
			if err != nil {
				t.Fatalf("%s, %s: the reference refuses the writer's stream: %v", name, cname, err)
			}

			if !bytes.Equal(out.Data, data) {
				t.Fatalf("%s, %s: the reference decodes %d bytes, want %d", name, cname, len(out.Data), len(data))
			}

			if out.Read != len(stream) {
				t.Fatalf("%s, %s: the reference read %d of %d bytes", name, cname, out.Read, len(stream))
			}

			if wantMarker := c.marker; out.Marker != wantMarker {
				t.Fatalf("%s, %s: end marker %v, want %v", name, cname, out.Marker, wantMarker)
			}

			if out.Corrupted {
				t.Fatalf("%s, %s: the reference flags the range coder's state", name, cname)
			}
		}
	}
}

// TestContractClassicReader: the reader decodes every stream of the corpus
// to what the reference does, consuming exactly the stream.
func TestContractClassicReader(t *testing.T) {
	t.Parallel()

	for name, data := range corpus(t) {
		for cname, c := range writerConfigs(len(data)) {
			stream := classic(t, c, data)

			in := bytes.NewReader(stream)

			r, err := lzma.NewReader(in)
			if err != nil {
				t.Fatalf("%s, %s: NewReader: %v", name, cname, err)
			}

			got, err := io.ReadAll(r)
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("%s, %s: %d bytes, %v", name, cname, len(got), err)
			}

			if in.Len() != 0 {
				t.Fatalf("%s, %s: %d bytes of the stream left unread", name, cname, in.Len())
			}
		}
	}
}

// lzma2 writes data as a chunk sequence, flushing at each cut so the
// sequence has several chunks and a chunk boundary inside the data.
func lzma2(tb testing.TB, c w2case, data []byte, cuts ...int) []byte {
	tb.Helper()

	var buf bytes.Buffer

	w, err := lzma.NewWriter2(&buf, c.opts...)
	if err != nil {
		tb.Fatal(err)
	}

	pos := 0

	for _, cut := range cuts {
		if _, err = w.Write(data[pos:cut]); err != nil {
			tb.Fatal(err)
		}

		if err = w.Flush(); err != nil {
			tb.Fatal(err)
		}

		pos = cut
	}

	if _, err = w.Write(data[pos:]); err != nil {
		tb.Fatal(err)
	}

	if err = w.Close(); err != nil {
		tb.Fatal(err)
	}

	return buf.Bytes()
}

// writer2Configs are the LZMA2 writer's configurations.
func writer2Configs() map[string]w2case {
	return map[string]w2case{
		"default":    {},
		"small dict": {opts: []lzma.Option{lzma.WithDictSize(lzma.MinDictSize)}, dict: lzma.MinDictSize},
		"lc0 lp4":    {opts: []lzma.Option{lzma.WithProperties(0, 4, 2)}},
		"lc4 pb4":    {opts: []lzma.Option{lzma.WithProperties(4, 0, 4)}},
	}
}

// cuts are where the LZMA2 corpus streams are flushed: none, one in the
// middle, several close together.
func cuts(n int) [][]int {
	if n < 8 {
		return [][]int{nil}
	}

	return [][]int{nil, {n / 2}, {1, 2, n / 3, n/3 + 1, n - 1}}
}

// TestContractLZMA2Writer: every chunk sequence the writer produces, cut
// where Flush was called, is what the reference decodes to the input
// under liblzma's chunk rules, and ends with the end chunk.
func TestContractLZMA2Writer(t *testing.T) {
	t.Parallel()

	for name, data := range corpus(t) {
		for cname, c := range writer2Configs() {
			for _, cut := range cuts(len(data)) {
				stream := lzma2(t, c, data, cut...)

				got, err := lzmaspec.DecodeLZMA2(stream, uint32(c.dictSize()))
				if err != nil || !bytes.Equal(got, data) {
					t.Fatalf("%s, %s, cuts %v: the reference: %d bytes, %v", name, cname, cut, len(got), err)
				}
			}
		}
	}
}

// TestContractLZMA2Reader: the reader decodes every chunk sequence of the
// corpus to the input, reports the end chunk, and reads exactly the
// sequence.
func TestContractLZMA2Reader(t *testing.T) {
	t.Parallel()

	for name, data := range corpus(t) {
		for cname, c := range writer2Configs() {
			for _, cut := range cuts(len(data)) {
				stream := lzma2(t, c, data, cut...)
				in := bytes.NewReader(stream)

				r, err := lzma.NewReader2(in, c.dictSize())
				if err != nil {
					t.Fatalf("%s, %s: NewReader2: %v", name, cname, err)
				}

				got, err := io.ReadAll(r)
				if err != nil || !bytes.Equal(got, data) {
					t.Fatalf("%s, %s, cuts %v: %d bytes, %v", name, cname, cut, len(got), err)
				}

				if !r.EOS() || in.Len() != 0 {
					t.Fatalf("%s, %s: EOS %v, %d bytes left", name, cname, r.EOS(), in.Len())
				}
			}
		}
	}
}

// verdict is what a decoder made of an input: the bytes, or the failure,
// and the input itself for the one exception classify makes.
type verdict struct {
	data   []byte
	err    error
	stream []byte
}

// lclpAbove4 reports whether a classic stream's properties byte asks for
// lc+lp above four, the one stream the reference decodes and the reader
// refuses.
func lclpAbove4(stream []byte) bool {
	if len(stream) < 5 {
		return false
	}

	p, err := lzmaspec.ParseProperties(stream[:5])

	return err == nil && p.LC+p.LP > 4
}

// classify requires the reader's verdict to follow the reference's: where
// the reference decodes, the same bytes; where it refuses, a refusal that
// matches one of the package's sentinels.
func classify(t *testing.T, what string, ref, got verdict) {
	t.Helper()

	if ref.err == nil {
		// The one place the reader is stricter than the SDK's decoder:
		// lc+lp above four, which liblzma refuses as unsupported.
		if errors.Is(got.err, lzma.ErrUnsupported) && lclpAbove4(ref.stream) {
			return
		}

		if got.err != nil || !bytes.Equal(got.data, ref.data) {
			t.Fatalf(
				"%s: the reference decodes %d bytes; the reader: %d bytes, %v",
				what,
				len(ref.data),
				len(got.data),
				got.err,
			)
		}

		return
	}

	switch {
	case got.err == nil:
		t.Fatalf("%s: the reference refuses (%v); the reader decodes %d bytes", what, ref.err, len(got.data))
	case errors.Is(got.err, lzma.ErrCorrupt), errors.Is(got.err, lzma.ErrUnsupported),
		errors.Is(got.err, io.ErrUnexpectedEOF):
	default:
		t.Fatalf("%s: the reader's error matches no sentinel: %v", what, got.err)
	}
}

func classicVerdict(stream []byte) verdict {
	// The dictionary cap is the reader's own limit, held to by its own
	// test; the differential lets a flipped header declare any size.
	r, err := lzma.NewReader(bytes.NewReader(stream), lzma.WithMaxDictSize(lzma.MaxDictSize))
	if err != nil {
		return verdict{err: err}
	}

	data, err := io.ReadAll(r)

	return verdict{data: data, err: err}
}

func referenceVerdict(stream []byte) verdict {
	out, err := lzmaspec.Decode(stream)

	return verdict{data: out.Data, err: err, stream: stream}
}

// malformed runs every single-byte flip and every truncation of stream
// through ref and got, and classifies each.
func malformed(t *testing.T, name string, stream []byte, ref, got func([]byte) verdict) {
	t.Helper()

	bad := make([]byte, len(stream))

	for i := range stream {
		for _, mask := range []byte{0x01, 0x80, 0xff} {
			copy(bad, stream)
			bad[i] ^= mask

			classify(t, name+": byte "+itoa(i)+" flipped", ref(bad), got(bad))
		}
	}

	for n := range stream {
		classify(t, name+": cut at "+itoa(n), ref(stream[:n]), got(stream[:n]))
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}

	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}

	return string(b)
}

// TestContractClassicMalformed: on every byte flip and every truncation of
// the corpus's classic streams, the reader follows the reference's verdict.
func TestContractClassicMalformed(t *testing.T) {
	t.Parallel()

	for name, data := range corpus(t) {
		if len(data) > 1000 {
			data = data[:1000]
		}

		for cname, c := range writerConfigs(len(data)) {
			malformed(t, name+", "+cname, classic(t, c, data), referenceVerdict, classicVerdict)
		}
	}
}

func lzma2Verdict(dictCap int) func([]byte) verdict {
	return func(stream []byte) verdict {
		r, err := lzma.NewReader2(bytes.NewReader(stream), dictCap)
		if err != nil {
			return verdict{err: err}
		}

		data, err := io.ReadAll(r)

		return verdict{data: data, err: err}
	}
}

func referenceLZMA2Verdict(dictCap int) func([]byte) verdict {
	return func(stream []byte) verdict {
		data, err := lzmaspec.DecodeLZMA2(stream, uint32(dictCap))

		return verdict{data: data, err: err}
	}
}

// stalled serves the first bytes of a stream and then answers every Read
// with no byte and no error, which io.Reader allows and asks callers to
// take as nothing having happened.
type stalled struct {
	data []byte
}

func (s *stalled) Read(p []byte) (int, error) {
	n := copy(p, s.data)
	s.data = s.data[n:]

	return n, nil
}

// TestContractInputMakingNoProgress: an input that goes on answering with
// no byte and no error ends the reader with io.ErrNoProgress, wherever in
// the stream it stalls: in the header, in a chunk header, in a chunk.
func TestContractInputMakingNoProgress(t *testing.T) {
	t.Parallel()

	data, ok := corpus(t)["mixed"]
	if !ok {
		t.Fatal("no mixed corpus")
	}

	c := w2case{dict: lzma.MinDictSize, opts: []lzma.Option{lzma.WithDictSize(lzma.MinDictSize)}}

	for name, tc := range map[string]struct {
		stream []byte
		read   func(io.Reader) error
	}{
		"classic": {classic(t, wcase{}, data), func(in io.Reader) error {
			r, err := lzma.NewReader(in)
			if err != nil {
				return err
			}

			_, err = io.Copy(io.Discard, r)

			return err
		}},
		"LZMA2": {lzma2(t, c, data), func(in io.Reader) error {
			r, err := lzma.NewReader2(in, c.dictSize())
			if err != nil {
				return err
			}

			_, err = io.Copy(io.Discard, r)

			return err
		}},
	} {
		for _, served := range []int{0, 1, 3, 12, 20, len(tc.stream) / 2, len(tc.stream) - 1} {
			done := make(chan error, 1)

			go func() { done <- tc.read(&stalled{data: tc.stream[:served]}) }()

			select {
			case err := <-done:
				if !errors.Is(err, io.ErrNoProgress) {
					t.Errorf("%s, stalled after %d bytes: %v, want io.ErrNoProgress", name, served, err)
				}
			case <-time.After(20 * time.Second):
				t.Fatalf("%s, stalled after %d bytes: the reader is still asking", name, served)
			}
		}
	}

	// The input Reset gives a Reader2 is bounded as the first one is.
	sequence := lzma2(t, c, data)

	r, err := lzma.NewReader2(bytes.NewReader(sequence), c.dictSize())
	if err != nil {
		t.Fatal(err)
	}

	if _, err = io.Copy(io.Discard, r); err != nil {
		t.Fatal(err)
	}

	for _, served := range []int{0, 1, 3, len(sequence) / 2, len(sequence) - 1} {
		done := make(chan error, 1)

		go func() {
			r.Reset(&stalled{data: sequence[:served]})

			_, rerr := io.Copy(io.Discard, r)
			done <- rerr
		}()

		select {
		case rerr := <-done:
			if !errors.Is(rerr, io.ErrNoProgress) {
				t.Errorf("after Reset, stalled after %d bytes: %v, want io.ErrNoProgress", served, rerr)
			}
		case <-time.After(20 * time.Second):
			t.Fatalf("after Reset, stalled after %d bytes: the reader is still asking", served)
		}
	}
}

// hesitant serves a stream a few bytes at a time and answers with no byte
// and no error a number of times before each: slow, and not stalled.
type hesitant struct {
	data                []byte
	every, pauses, owed int
}

func (h *hesitant) Read(p []byte) (int, error) {
	if h.owed > 0 {
		h.owed--

		return 0, nil
	}

	if len(h.data) == 0 {
		return 0, io.EOF
	}

	n := copy(p[:min(len(p), h.every)], h.data)
	h.data = h.data[n:]
	h.owed = h.pauses

	return n, nil
}

// TestContractInputPausingIsRead: only answers with nothing in a row count
// against an input. One that pauses before every seven bytes, once or
// ninety-nine times, is read whole however many pauses that makes.
func TestContractInputPausingIsRead(t *testing.T) {
	t.Parallel()

	data, ok := corpus(t)["mixed"]
	if !ok {
		t.Fatal("no mixed corpus")
	}

	c := w2case{dict: lzma.MinDictSize, opts: []lzma.Option{lzma.WithDictSize(lzma.MinDictSize)}}

	for _, pauses := range []int{1, 99} {
		r, err := lzma.NewReader(&hesitant{data: classic(t, wcase{}, data), every: 7, pauses: pauses})
		if err != nil {
			t.Fatalf("classic, %d pauses: %v", pauses, err)
		}

		if got, rerr := io.ReadAll(r); rerr != nil || !bytes.Equal(got, data) {
			t.Errorf("classic, %d pauses: %d bytes, %v", pauses, len(got), rerr)
		}

		r2, err := lzma.NewReader2(&hesitant{data: lzma2(t, c, data), every: 7, pauses: pauses}, c.dictSize())
		if err != nil {
			t.Fatalf("LZMA2, %d pauses: %v", pauses, err)
		}

		if got, rerr := io.ReadAll(r2); rerr != nil || !bytes.Equal(got, data) {
			t.Errorf("LZMA2, %d pauses: %d bytes, %v", pauses, len(got), rerr)
		}
	}
}

// TestContractLZMA2Malformed: on every byte flip and every truncation of
// the corpus's chunk sequences, the reader follows the reference's verdict
// under liblzma's chunk rules.
func TestContractLZMA2Malformed(t *testing.T) {
	t.Parallel()

	for name, data := range corpus(t) {
		if len(data) > 1000 {
			data = data[:1000]
		}

		for cname, c := range writer2Configs() {
			stream := lzma2(t, c, data, cuts(len(data))[len(cuts(len(data)))-1]...)
			malformed(t, name+", "+cname, stream, referenceLZMA2Verdict(c.dictSize()), lzma2Verdict(c.dictSize()))
		}
	}
}

// miscounter is a writer whose nth Write breaks io.Writer's contract with
// no error, or the first one after it that is given anything: by delta
// below zero it takes that many bytes fewer than it was given and says so,
// above zero it takes them all and reports more.
type miscounter struct {
	calls, at, delta int
	hit              bool
	// hitAt is the call that was miscounted, once hit is set.
	hitAt int
}

func (m *miscounter) Write(p []byte) (int, error) {
	m.calls++
	// An empty write has no count to get wrong.
	if m.calls >= m.at && !m.hit && len(p) > 0 {
		m.hit = true
		m.hitAt = m.calls

		return len(p) + m.delta, nil
	}

	return len(p), nil
}

// TestContractWriter2HoldsItsWriterToItsCount: a chunk sequence goes out as
// headers and chunks, each a write counted on. An underlying writer that
// takes less than it was given without an error, at any one of its calls,
// is io.ErrShortWrite from the Write, Flush or Close that meets it, a
// chunk header as much as a chunk. One that reports more than it was given
// is an error, not a panic. The data does not compress, and goes out in
// chunks of both kinds: compressed all the same under the smallest
// dictionary, which no longer holds a chunk's input to copy it from, and
// stored under one that does.
func TestContractWriter2HoldsItsWriterToItsCount(t *testing.T) {
	t.Parallel()

	data := corpus(t)["random"]
	if len(data) == 0 {
		t.Fatal("no random corpus")
	}

	for _, dictSize := range []int{lzma.MinDictSize, 1 << 16} {
		writer2HoldsItsWriterToItsCount(t, data, dictSize)
	}
}

func writer2HoldsItsWriterToItsCount(t *testing.T, data []byte, dictSize int) {
	t.Helper()

	for _, delta := range []int{-1, 3} {
		var calls int

		for at := 1; ; at++ {
			m := &miscounter{at: at, delta: delta}

			err := func() error {
				w, err := lzma.NewWriter2(m, lzma.WithDictSize(dictSize))
				if err != nil {
					return err
				}

				for _, piece := range [][]byte{data[:len(data)/2], data[len(data)/2:]} {
					if _, err = w.Write(piece); err != nil {
						return err
					}

					if err = w.Flush(); err != nil {
						return err
					}
				}

				return w.Close()
			}()

			if !m.hit {
				calls = at - 1

				break
			}

			if delta < 0 && !errors.Is(err, io.ErrShortWrite) {
				t.Errorf("dictionary %d, call %d a byte short: %v, want io.ErrShortWrite", dictSize, at, err)
			}

			if delta > 0 && err == nil {
				t.Errorf("dictionary %d, call %d counting %d too many: no error", dictSize, at, delta)
			}
		}

		if calls < 5 {
			t.Errorf("dictionary %d: the sequence went out in %d writes, too few to say anything", dictSize, calls)
		}
	}
}

// failingByteWriter is an io.ByteWriter, which the classic writer writes to
// as it is, a byte at a time. Its nth WriteByte fails, that one only; what
// arrives through Write, the header, is taken and not counted.
type failingByteWriter struct {
	at, calls, after int
	err              error
}

func (*failingByteWriter) Write(p []byte) (int, error) { return len(p), nil }

func (f *failingByteWriter) WriteByte(byte) error {
	f.calls++

	switch {
	case f.calls == f.at:
		return f.err
	case f.calls > f.at:
		f.after++
	}

	return nil
}

// TestContractWriterKeepsItsWritersFailure: Close "ensures that all data
// from the buffer will be compressed and the LZMA stream will be finished.
// After a failed Write it returns that error and writes nothing". The
// stream leaves the encoder a byte at a time, from the midst of any
// operation's bits, during Write once the dictionary has filled and during
// Close for what is left, the end marker and the range coder's last bytes.
// Whichever byte the underlying writer refuses, once, the call that met it
// returns that error, Close returns it, and no byte follows the one
// refused: a stream short of a byte is never finished as if it were whole.
func TestContractWriterKeepsItsWritersFailure(t *testing.T) {
	t.Parallel()

	// Records and prose for operations of every kind, then zeros to fill
	// the smallest dictionary and its buffer, so that the first part is
	// compressed during Write.
	data := append(append(records(1024), prose(1024)...), make([]byte, 7000)...)
	refused := errors.New("refused")

	var during [2]int

	for at := 1; ; at++ {
		f := &failingByteWriter{at: at, err: refused}

		w, err := lzma.NewWriter(f, lzma.WithDictSize(lzma.MinDictSize))
		if err != nil {
			t.Fatal(err)
		}

		_, werr := w.Write(data)
		inWrite := f.calls >= at
		cerr := w.Close()

		if f.calls < at {
			// The stream is shorter than at bytes: every byte was refused once.
			if werr != nil || cerr != nil {
				t.Fatalf("nothing refused: Write %v, Close %v", werr, cerr)
			}

			break
		}

		if inWrite {
			during[0]++
		} else {
			during[1]++
		}

		if inWrite != (werr != nil) || (werr != nil && !errors.Is(werr, refused)) {
			t.Errorf("byte %d refused, during Write: %v: Write returned %v", at, inWrite, werr)
		}

		if !errors.Is(cerr, refused) {
			t.Errorf("byte %d refused: Close returned %v", at, cerr)
		}

		if f.after != 0 {
			t.Errorf("byte %d refused: %d bytes written after it", at, f.after)
		}
	}

	if during[0] < 100 || during[1] < 5 {
		t.Errorf("%d bytes left during Write and %d during Close: too few to say anything", during[0], during[1])
	}
}

// TestContractPropertiesPrint: Properties print as "LC 3 LP 0 PB 2", as a
// value and inside a Header, which is how a caller that logs what Header
// returned sees them.
func TestContractPropertiesPrint(t *testing.T) {
	t.Parallel()

	p := lzma.Properties{LC: 3, LP: 0, PB: 2}
	if got := fmt.Sprint(p); got != "LC 3 LP 0 PB 2" {
		t.Errorf("a value prints as %q", got)
	}

	r, err := lzma.NewReader(bytes.NewReader(classic(t, wcase{}, []byte("hello"))))
	if err != nil {
		t.Fatal(err)
	}

	if got := fmt.Sprintf("%v", r.Header()); !strings.Contains(got, "LC 3 LP 0 PB 2") {
		t.Errorf("a header prints as %q", got)
	}
}

// TestContractProperties: every properties byte decodes as the
// specification says and encodes back to itself, and the codes beyond the
// 225 the format defines are refused as it refuses them; lc+lp above four
// is refused as unsupported, liblzma's rule.
func TestContractProperties(t *testing.T) {
	t.Parallel()

	for code := range 256 {
		ref, refErr := lzmaspec.ParseProperties([]byte{byte(code), 0, 0, 0, 0})
		p, err := lzma.PropertiesForCode(byte(code))

		switch {
		case refErr != nil:
			if !errors.Is(err, lzma.ErrCorrupt) {
				t.Errorf("code %d: the specification refuses it; the package: %v", code, err)
			}
		case ref.LC+ref.LP > 4:
			if !errors.Is(err, lzma.ErrUnsupported) {
				t.Errorf("code %d: lc+lp %d; want ErrUnsupported, got %v", code, ref.LC+ref.LP, err)
			}
		default:
			if err != nil || uint(p.LC) != ref.LC || uint(p.LP) != ref.LP ||
				uint(p.PB) != ref.PB {
				t.Errorf("code %d: %+v, %v; want lc %d lp %d pb %d", code, p, err, ref.LC, ref.LP, ref.PB)
			} else if p.Code() != byte(
				code,
			) {
				t.Errorf("code %d encodes back to %d", code, p.Code())
			}
		}
	}
}

// TestContractHeaderDictSize: "a classic header states the smallest size XZ
// Utils reads that holds it: one of the form 2^n or 2^n+2^(n-1)", the top
// of the range being internal/dictsize's own test's. Whatever dictionary the
// writer is given, its header passes the package's own sniff, which is XZ
// Utils' rule for the field, and the stream reads back.
func TestContractHeaderDictSize(t *testing.T) {
	t.Parallel()

	data, ok := corpus(t)["mixed"]
	if !ok {
		t.Fatal("no mixed corpus")
	}

	for dict, want := range map[int]uint32{
		lzma.MinDictSize:     1 << 12,
		lzma.MinDictSize + 1: 1<<12 + 1<<11,
		5000:                 1<<12 + 1<<11,
		1<<12 + 1<<11:        1<<12 + 1<<11,
		1<<12 + 1<<11 + 1:    1 << 13,
		1 << 20:              1 << 20,
		1<<20 + 1:            1<<20 + 1<<19,
		1<<24 - 1:            1 << 24,
	} {
		stream := classic(t, wcase{opts: []lzma.Option{lzma.WithDictSize(dict)}}, data)

		if got := binary.LittleEndian.Uint32(stream[1:]); got != want {
			t.Errorf("dictionary %d: the header states %d, want %d", dict, got, want)
		}

		if !lzma.ValidHeader(stream[:lzma.HeaderLen]) {
			t.Errorf("dictionary %d: the header does not pass ValidHeader", dict)
		}

		r, err := lzma.NewReader(bytes.NewReader(stream))
		if err != nil {
			t.Fatalf("dictionary %d: %v", dict, err)
		}

		if got, rerr := io.ReadAll(r); rerr != nil || !bytes.Equal(got, data) {
			t.Errorf("dictionary %d: %d bytes read back, %v", dict, len(got), rerr)
		}
	}

	// The encoder keeps the size it was given: 5500 random bytes written
	// twice under a dictionary of 5000 cannot have the second copy refer
	// to the first, 5500 back, and the stream is about both; a dictionary
	// of the 6144 the header states would halve it.
	random, ok := corpus(t)["random"]
	if !ok {
		t.Fatal("no random corpus")
	}

	twice := append(bytes.Clone(random[:5500]), random[:5500]...)
	if stream := classic(t, wcase{opts: []lzma.Option{lzma.WithDictSize(5000)}}, twice); len(stream) < 10000 {
		t.Errorf("11000 bytes, the second half a copy 5500 back, in %d under a dictionary of 5000", len(stream))
	}

	// A reader's limit is held against what the header states.
	stream := classic(t, wcase{opts: []lzma.Option{lzma.WithDictSize(5000)}}, data)

	if _, err := lzma.NewReader(bytes.NewReader(stream), lzma.WithMaxDictSize(5000)); !errors.Is(
		err, lzma.ErrUnsupported,
	) {
		t.Errorf("a limit of 5000 on a header stating 6144: %v, want ErrUnsupported", err)
	}

	if _, err := lzma.NewReader(bytes.NewReader(stream), lzma.WithMaxDictSize(6144)); err != nil {
		t.Errorf("a limit of 6144 on a header stating 6144: %v", err)
	}
}

// TestContractSizeTheReaderTakes: WithSize is "at most a pebibyte, the
// largest the reader takes": the writer refuses to declare what its own
// reader would refuse to read.
func TestContractSizeTheReaderTakes(t *testing.T) {
	t.Parallel()

	if _, err := lzma.NewWriter(io.Discard, lzma.WithSize(1<<50)); err != nil {
		t.Errorf("a pebibyte: %v", err)
	}

	for _, size := range []int64{1<<50 + 1, 1<<63 - 1} {
		var out bytes.Buffer

		if _, err := lzma.NewWriter(&out, lzma.WithSize(size)); err == nil || out.Len() != 0 {
			t.Errorf("size %d: %v, %d bytes written", size, err, out.Len())
		}
	}
}

// TestContractValidHeader: the format sniff accepts the dictionary sizes
// the reference encoders emit, at 2^10 and up, 2^32-1 included, and an
// explicit size to 256 GiB; it refuses other sizes and a wrong length.
func TestContractValidHeader(t *testing.T) {
	t.Parallel()

	header := func(dictSize uint32, size int64) []byte {
		h := make([]byte, lzma.HeaderLen)
		h[0] = 0x5D

		for i := range 4 {
			h[1+i] = byte(dictSize >> (8 * i))
		}

		for i := range 8 {
			h[5+i] = byte(uint64(size) >> (8 * i))
		}

		return h
	}

	for _, dictSize := range []uint32{1 << 10, 1<<10 + 1<<9, 1 << 20, 1<<20 + 1<<19, 1<<32 - 1} {
		if !lzma.ValidHeader(header(dictSize, -1)) {
			t.Errorf("dictionary size %d refused", dictSize)
		}
	}

	for _, dictSize := range []uint32{0, 1 << 9, 1<<10 + 1, 1<<10 + 1<<9 + 1, 1<<32 - 2} {
		if lzma.ValidHeader(header(dictSize, -1)) {
			t.Errorf("dictionary size %d accepted", dictSize)
		}
	}

	if !lzma.ValidHeader(header(1<<20, 1<<38)) || lzma.ValidHeader(header(1<<20, 1<<38+1)) {
		t.Error("the explicit size bound is not 256 GiB")
	}

	if lzma.ValidHeader(header(1<<20, -1)[:12]) || lzma.ValidHeader(append(header(1<<20, -1), 0)) {
		t.Error("a header of the wrong length accepted")
	}
}

// TestContractZeroOption: "the zero Option sets nothing": each constructor
// given one makes what it makes given none, and still refuses another's
// option beside it.
func TestContractZeroOption(t *testing.T) {
	t.Parallel()

	data := []byte("the zero option sets nothing")

	want := classic(t, wcase{}, data)
	if got := classic(t, wcase{opts: []lzma.Option{{}}}, data); !bytes.Equal(got, want) {
		t.Error("NewWriter given the zero Option writes another stream")
	}

	want2 := lzma2(t, w2case{}, data)
	if got := lzma2(t, w2case{opts: []lzma.Option{{}}}, data); !bytes.Equal(got, want2) {
		t.Error("NewWriter2 given the zero Option writes another sequence")
	}

	r, err := lzma.NewReader(bytes.NewReader(want), lzma.Option{})
	if err != nil {
		t.Fatal(err)
	}

	if got, rerr := io.ReadAll(r); rerr != nil || !bytes.Equal(got, data) {
		t.Errorf("NewReader given the zero Option: %q, %v", got, rerr)
	}

	if _, err = lzma.NewReader(bytes.NewReader(want), lzma.Option{}, lzma.WithSize(1)); err == nil {
		t.Error("NewReader accepted WithSize beside the zero Option")
	}
}

// TestContractOptionBounds: the readers' and writers' options are accepted
// at the bounds of the dictionary size and the properties, and refused one
// past them.
func TestContractOptionBounds(t *testing.T) {
	t.Parallel()

	stream := classic(t, wcase{opts: []lzma.Option{lzma.WithDictSize(lzma.MinDictSize)}}, []byte("hello"))
	sequence := lzma2Stream(t, []byte("hello"))

	// A reader grows its dictionary as the data needs it, so both bounds are
	// constructed; a writer allocates the whole dictionary on construction,
	// four gigabytes at the upper bound, so the writers are held at the
	// lower one.
	for _, dictSize := range []int{lzma.MinDictSize, lzma.MaxDictSize} {
		if _, err := lzma.NewReader(bytes.NewReader(stream), lzma.WithMaxDictSize(int64(dictSize))); err != nil {
			t.Errorf("WithMaxDictSize %d: %v; want accepted", dictSize, err)
		}

		if _, err := lzma.NewReader2(bytes.NewReader(sequence), dictSize); err != nil {
			t.Errorf("NewReader2 dictionary %d: %v; want accepted", dictSize, err)
		}
	}

	if _, err := lzma.NewWriter(io.Discard, lzma.WithDictSize(lzma.MinDictSize)); err != nil {
		t.Errorf("writer WithDictSize %d: %v; want accepted", lzma.MinDictSize, err)
	}

	if _, err := lzma.NewWriter2(io.Discard, lzma.WithDictSize(lzma.MinDictSize)); err != nil {
		t.Errorf("LZMA2 writer WithDictSize %d: %v; want accepted", lzma.MinDictSize, err)
	}

	for _, dictSize := range []int{1, lzma.MinDictSize - 1, lzma.MaxDictSize + 1} {
		if _, err := lzma.NewReader(bytes.NewReader(stream), lzma.WithMaxDictSize(int64(dictSize))); err == nil {
			t.Errorf("WithMaxDictSize %d accepted", dictSize)
		}

		if _, err := lzma.NewReader2(bytes.NewReader(sequence), dictSize); err == nil {
			t.Errorf("NewReader2 dictionary %d accepted", dictSize)
		}

		if _, err := lzma.NewWriter(io.Discard, lzma.WithDictSize(dictSize)); err == nil {
			t.Errorf("writer WithDictSize %d accepted", dictSize)
		}

		if _, err := lzma.NewWriter2(io.Discard, lzma.WithDictSize(dictSize)); err == nil {
			t.Errorf("LZMA2 writer WithDictSize %d accepted", dictSize)
		}
	}

	for _, p := range [][3]int{{4, 0, 4}, {0, 4, 0}, {2, 2, 2}} {
		if _, err := lzma.NewWriter(io.Discard, lzma.WithProperties(p[0], p[1], p[2])); err != nil {
			t.Errorf("properties %v: %v; want accepted", p, err)
		}
	}

	for _, p := range [][3]int{{9, 0, 0}, {-1, 0, 0}, {0, 5, 0}, {0, -1, 0}, {0, 0, 5}, {0, 0, -1}, {5, 4, 0}} {
		if _, err := lzma.NewWriter(io.Discard, lzma.WithProperties(p[0], p[1], p[2])); err == nil {
			t.Errorf("properties %v accepted by the writer", p)
		}

		if _, err := lzma.NewWriter2(io.Discard, lzma.WithProperties(p[0], p[1], p[2])); err == nil {
			t.Errorf("properties %v accepted by the LZMA2 writer", p)
		}
	}

	// An option of the other constructor is refused, not ignored.
	if _, err := lzma.NewReader(bytes.NewReader(stream), lzma.WithDictSize(lzma.MinDictSize)); err == nil {
		t.Error("the reader accepted WithDictSize")
	}

	if _, err := lzma.NewWriter2(io.Discard, lzma.WithSize(5)); err == nil {
		t.Error("the LZMA2 writer accepted WithSize")
	}

	if _, err := lzma.NewWriter(io.Discard, lzma.WithMaxDictSize(lzma.MinDictSize)); err == nil {
		t.Error("the writer accepted WithMaxDictSize")
	}
}

// TestContractOutputWithinDeclaredSize: nothing past the declared size is
// handed out. A stream whose header says sixteen bytes and whose second
// operation is a match that crosses them is ErrCorrupt, "sizes that disagree
// with the data", and the bytes read before the error are at most sixteen
// of the data. The same holds for an LZMA2 chunk, whose header carries the
// size.
func TestContractOutputWithinDeclaredSize(t *testing.T) {
	t.Parallel()

	const declared = 16

	data := bytes.Repeat([]byte("abcdefgh"), 65)

	check := func(t *testing.T, r io.Reader) {
		t.Helper()

		got, err := io.ReadAll(r)
		if !errors.Is(err, lzma.ErrCorrupt) {
			t.Errorf("%v, want an error matching ErrCorrupt", err)
		}

		if len(got) > declared || !bytes.HasPrefix(data, got) {
			t.Errorf("%d bytes read from a stream declaring %d: %q", len(got), declared, got)
		}
	}

	t.Run("classic", func(t *testing.T) {
		t.Parallel()

		stream := classic(t, wcase{}, data)
		binary.LittleEndian.PutUint64(stream[5:], declared)

		r, err := lzma.NewReader(bytes.NewReader(stream))
		if err != nil {
			t.Fatal(err)
		}

		check(t, r)
	})

	t.Run("an LZMA2 chunk", func(t *testing.T) {
		t.Parallel()

		c := w2case{}
		chunks := lzma2(t, c, data)
		// The chunk's uncompressed size, less one: five bits of the control
		// byte, then two bytes.
		chunks[0] &^= 0x1f
		binary.BigEndian.PutUint16(chunks[1:], declared-1)

		r, err := lzma.NewReader2(bytes.NewReader(chunks), c.dictSize())
		if err != nil {
			t.Fatal(err)
		}

		check(t, r)
	})
}

// TestContractFlushedChunkIsRead: Flush "writes all buffered data out to the
// underlying stream", and a reader at the other end of it reads that data
// without waiting for what the writer sends next: Read returns what is
// there, as readers do.
func TestContractFlushedChunkIsRead(t *testing.T) {
	t.Parallel()

	pr, pw := io.Pipe()
	next := make(chan struct{})
	written := make(chan error, 1)

	go func() {
		written <- func() error {
			w, err := lzma.NewWriter2(pw)
			if err != nil {
				return err
			}

			if _, err = w.Write([]byte("hello")); err != nil {
				return err
			}

			if err = w.Flush(); err != nil {
				return err
			}

			<-next

			if _, err = w.Write([]byte("world")); err != nil {
				return err
			}

			if err = w.Close(); err != nil {
				return err
			}

			return pw.Close()
		}()
	}()

	r, err := lzma.NewReader2(pr, w2case{}.dictSize())
	if err != nil {
		t.Fatal(err)
	}

	type read struct {
		data string
		err  error
	}

	first := make(chan read, 1)

	go func() {
		buf := make([]byte, 4<<10)

		n, rerr := r.Read(buf)
		first <- read{string(buf[:n]), rerr}
	}()

	select {
	case got := <-first:
		if got.data != "hello" || got.err != nil {
			t.Errorf("the first Read: %q, %v", got.data, got.err)
		}

		close(next)
	case <-time.After(10 * time.Second):
		close(next)
		t.Fatal("Read waits for more than the flushed chunk")
	}

	if rest, rerr := io.ReadAll(r); string(rest) != "world" || rerr != nil {
		t.Errorf("after it: %q, %v", rest, rerr)
	}

	if werr := <-written; werr != nil {
		t.Fatal(werr)
	}
}

// TestContractMaxDictSize: a classic header declaring a dictionary above
// WithMaxDictSize is refused as unsupported; one at the limit is read. "The
// default is no bound below the format's 4 GiB-1": a header stating 2 GiB,
// or the format's largest, opens with no option, and costs what its five
// bytes of data do.
func TestContractMaxDictSize(t *testing.T) {
	t.Parallel()

	stream := classic(t, wcase{opts: []lzma.Option{lzma.WithDictSize(1 << 20)}}, []byte("hello"))

	for _, stated := range []uint32{1 << 31, 1<<32 - 1} {
		large := bytes.Clone(stream)
		binary.LittleEndian.PutUint32(large[1:], stated)

		r, err := lzma.NewReader(bytes.NewReader(large))
		if err != nil {
			t.Fatalf("a header stating %d, no option: %v", stated, err)
		}

		if got, rerr := io.ReadAll(r); rerr != nil || string(got) != "hello" {
			t.Errorf("a header stating %d: %q, %v", stated, got, rerr)
		}

		if _, err = lzma.NewReader(bytes.NewReader(large), lzma.WithMaxDictSize(int64(stated)-1)); !errors.Is(
			err, lzma.ErrUnsupported,
		) {
			t.Errorf("a header stating %d under a limit one below: %v, want ErrUnsupported", stated, err)
		}
	}

	_, err := lzma.NewReader(bytes.NewReader(stream), lzma.WithMaxDictSize(1<<16))
	if !errors.Is(err, lzma.ErrUnsupported) {
		t.Fatalf("%v; want ErrUnsupported", err)
	}

	r, err := lzma.NewReader(bytes.NewReader(stream), lzma.WithMaxDictSize(1<<20))
	if err != nil {
		t.Fatalf("a limit equal to the header's size refuses: %v", err)
	}

	if got, err := io.ReadAll(r); err != nil || string(got) != "hello" {
		t.Fatalf("%q, %v", got, err)
	}
}

// TestContractDeclaredSize: ErrUnsupported is "a classic LZMA header
// declaring a stream larger than the implementation supports", whatever the
// size above a pebibyte, up to the largest the field holds short of "no
// size"; a pebibyte itself is read.
func TestContractDeclaredSize(t *testing.T) {
	t.Parallel()

	stream := classic(t, wcase{}, []byte("hello"))

	declare := func(size uint64) []byte {
		out := bytes.Clone(stream)
		binary.LittleEndian.PutUint64(out[5:], size)

		return out
	}

	if _, err := lzma.NewReader(bytes.NewReader(declare(1 << 50))); err != nil {
		t.Errorf("a pebibyte: %v", err)
	}

	for _, size := range []uint64{1<<50 + 1, 1<<63 - 1, 1 << 63, 1<<64 - 2} {
		_, err := lzma.NewReader(bytes.NewReader(declare(size)))
		if !errors.Is(err, lzma.ErrUnsupported) || errors.Is(err, lzma.ErrCorrupt) {
			t.Errorf("size %d: %v, want an error matching ErrUnsupported alone", size, err)
		}
	}
}

// TestContractChunkRules: the chunk sequence's rules from the reader's side,
// each sequence hand-written: the first chunk resets the dictionary, a
// dictionary reset demands properties, an uncompressed chunk without a
// reset continues, a chunk that resets the state is decoded from a new one,
// and the end chunk ends the sequence with nothing read past it.
func TestContractChunkRules(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	w, err := lzma.NewWriter2(&buf)
	if err != nil {
		t.Fatal(err)
	}

	// Long enough to compress, so the writer emits an LZMA chunk, not an
	// uncompressed one.
	data := bytes.Repeat([]byte("abcabcabc "), 50)

	if _, err = w.Write(data); err != nil {
		t.Fatal(err)
	}

	if err = w.Close(); err != nil {
		t.Fatal(err)
	}

	// The writer's stream: a cLRND chunk (0xE0) then the end byte.
	first := buf.Bytes()
	if first[0] != 0xE0 {
		t.Fatalf("the writer's first chunk is %#x, not cLRND", first[0])
	}

	chunk := first[:len(first)-1]

	// A chunk to stand before that one: 400 bytes, the last a line feed.
	dataBefore := bytes.Repeat([]byte("abcabcabc\n"), 40)

	before := lzma2Stream(t, dataBefore)
	if before[0] != 0xE0 {
		t.Fatalf("the writer's first chunk is %#x, not cLRND", before[0])
	}

	before = before[:len(before)-1]

	uncompressed := append([]byte{0x02, 0x00, 0x02}, []byte("xyz")...)      // cU, 3 bytes
	uncompressedReset := append([]byte{0x01, 0x00, 0x02}, []byte("xyz")...) // cUD, 3 bytes

	cases := []struct {
		name string
		in   []byte
		want []byte // nil: ErrCorrupt
	}{
		{"cL first", markControl(chunk, 0x80), nil},
		{"cLR first", markControl(chunk, 0xA0), nil},
		{"cLRN first", markControl(chunk, 0xC0), nil},
		{"cU first", append(bytes.Clone(uncompressed), 0x00), nil},
		{"cUD then cL", append(append(bytes.Clone(uncompressedReset), markControl(chunk, 0x80)...), 0x00), nil},
		{"cUD then cLR", append(append(bytes.Clone(uncompressedReset), markControl(chunk, 0xA0)...), 0x00), nil},
		// The chunk's bytes were coded in an empty dictionary, so only a
		// dictionary reset before them decodes them again.
		{
			"cUD then cLRND",
			append(append(bytes.Clone(uncompressedReset), chunk...), 0x00),
			append([]byte("xyz"), data...),
		},
		{
			"cLRND then cU then cLRND",
			append(append(append(bytes.Clone(chunk), uncompressed...), chunk...), 0x00),
			append(append(bytes.Clone(data), []byte("xyz")...), data...),
		},
		{"end then more", append(bytes.Clone(first), uncompressedReset...), data},
		// A state reset alone. The chunk's bytes were coded from a new state
		// at the start of a stream; after 400 bytes that end in a line feed
		// they stand where a new state reads them the same, at an offset
		// that is a multiple of four after a byte whose top three bits are
		// zero, and decode there only if the state is reset for them.
		{
			"cLRND then cLR",
			append(append(bytes.Clone(before), markControl(chunk, 0xA0)...), 0x00),
			append(bytes.Clone(dataBefore), data...),
		},
	}

	for _, c := range cases {
		in := bytes.NewReader(c.in)

		r, err := lzma.NewReader2(in, 8<<20)
		if err != nil {
			t.Fatalf("%s: NewReader2: %v", c.name, err)
		}

		got, err := io.ReadAll(r)

		switch {
		case c.want == nil:
			if !errors.Is(err, lzma.ErrCorrupt) {
				t.Errorf("%s: %d bytes, %v; want ErrCorrupt", c.name, len(got), err)
			}
		case err != nil || !bytes.Equal(got, c.want):
			t.Errorf("%s: %q, %v; want %q", c.name, got, err, c.want)
		case c.name == "end then more" && in.Len() != len(uncompressedReset):
			t.Errorf(
				"%s: %d bytes left unread; want the %d past the end chunk",
				c.name,
				in.Len(),
				len(uncompressedReset),
			)
		}

		ref, refErr := lzmaspec.DecodeLZMA2(c.in, 8<<20)

		refOK := refErr == nil
		if refOK != (c.want != nil) || (refOK && !bytes.Equal(ref, c.want)) {
			t.Errorf("%s: the reference says %q, %v; the case expects %q", c.name, ref, refErr, c.want)
		}
	}
}

// markControl is the chunk with its control byte's reset bits replaced,
// keeping the size bits; a properties byte the chunk has is kept and one it
// lacks is not needed by the controls used here.
func markControl(chunk []byte, control byte) []byte {
	c := bytes.Clone(chunk)
	c[0] = control | (chunk[0] & 0x1F)

	if control < 0xC0 {
		// no properties byte: drop it
		c = append(c[:5], c[6:]...)
	}

	return c
}

// TestContractHandBuiltStreams: "what the reference decodes, the readers
// decode to the same bytes", over streams the package's writer does not
// produce on demand. Each was built operation by operation with an encoder
// written for the purpose, which shares no code with the package; a row
// names the operations, and the reference is the witness of the bytes they
// decode to.
//
// The first two hold the state machine where its twelve states part: a
// short repeat sends a literal state to 9 and any other to 11, a repeat to
// 8 and to 11, and each state has probabilities of its own, so a wrong turn
// shows in the bytes that follow.
//
// The others hold the range coder's normalization at its threshold. Under
// lc 0, lp 4, pb 4 every bit of a stream's first literals meets a
// probability that is still one half, so the range halves exactly at each:
// it is 1<<27 after five literals, 1<<28 after four and 1<<29 after three,
// and exactly 1<<24, the least value that takes no new byte, after the
// third, fourth or fifth bit of the repeat that follows: the bit that says
// whether a repeat is the last distance, a short one, the second, the third.
// A decoder that takes a byte there has lost the stream. The last two hold
// the same threshold at the two bits that choose the tree a length is coded
// in, the third bit of a match after five literals and the fourth after
// four.
func TestContractHandBuiltStreams(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ name, stream, want string }{
		{
			"a short repeat after a match: lit a, short repeat, lit bcd, match of 2 at 3, short repeat, lit xyz",
			"0300000100ffffffffffffffff0030e278803f7d1a2ddee16bfa2d5ffff04c3000",
			"aabcdbcdxyz",
		},
		{
			"a repeat after a literal: lit a, repeat of 2, lit bcd, match of 2 at 3, short repeat, lit xyz",
			"0300000100ffffffffffffffff0030e8d3c803f7cf93e5fd24b9a882dffef4b400",
			"aaabcdbcdxyz",
		},
		{
			"the range at 1<<24 after the last-distance bit: lit abcde, short repeat, lit xyz",
			"d800000100ffffffffffffffff0030988866432e1e0f27a83dfffffffc200000",
			"abcdeexyz",
		},
		{
			"the range at 1<<24 after the short-repeat bit: lit abcd, short repeat, lit xyz",
			"d800000100ffffffffffffffff00309888664c3c1e4f507bfffffff8400000",
			"abcddxyz",
		},
		{
			"the range at 1<<24 after the second-distance bit: lit abcd, repeat of 3 at the second distance, lit xyz",
			"d800000100ffffffffffffffff00309888664e13c1e4f507bfffffff84000000",
			"abcddddxyz",
		},
		{
			"the range at 1<<24 after the third-distance bit: lit abc, repeat of 3 at the third distance, lit xyz",
			"d800000100ffffffffffffffff003098887e13c1e4f507bfffffff84000000",
			"abccccxyz",
		},
		{
			"the range at 1<<24 after a length's first choice bit: lit abcde, match of 2 at 1, lit xyz",
			"d800000100ffffffffffffffff0030988866432c001e0f27a8440ffffffbe10000",
			"abcdeeexyz",
		},
		{
			"the range at 1<<24 after a length's second choice bit: lit abcd, match of 10 at 2, lit xyz",
			"d800000100ffffffffffffffff00309888664a009e0f27e30470fffffbe30000",
			"abcdcdcdcdcdcdxyz",
		},
	} {
		stream, err := hex.DecodeString(tc.stream)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}

		out, err := lzmaspec.Decode(stream)
		if err != nil || string(out.Data) != tc.want || !out.Marker || out.Read != len(stream) {
			t.Fatalf("%s: the reference: %q, %v, marker %v, %d of %d bytes read; the row says %q",
				tc.name, out.Data, err, out.Marker, out.Read, len(stream), tc.want)
		}

		r, err := lzma.NewReader(bytes.NewReader(stream))
		if err != nil {
			t.Errorf("%s: NewReader: %v", tc.name, err)

			continue
		}

		if got, rerr := io.ReadAll(r); rerr != nil || string(got) != tc.want {
			t.Errorf("%s: %q, %v; the reference decodes %q", tc.name, got, rerr, tc.want)
		}
	}
}

// TestContractCarryIntoAHeldByte: "what the writers produce, the reference
// decodes to the input", at the one turn of the range encoder that random
// data takes about once in a billion bytes. The encoder holds back the byte
// it would write next, and every 0xff after it, until the bytes that follow
// can no longer carry into them. Here the carry comes while the byte moving
// out of the low word is itself 0xff: the carry belongs to the bytes held,
// and that byte is held in their place.
//
// The input was found by search over seeds; its 383rd byte brings the
// carry. No byte of it comes twice in a row and the encoder finds nothing
// in it to copy, so it is coded as literals, and for literals the format
// fixes every bit: the way to that carry does not pass through the match
// finder. Should an encoder one day find a match in it, the stream is no
// longer the one this row was chosen for; its length says so.
func TestContractCarryIntoAHeldByte(t *testing.T) {
	t.Parallel()

	src := rand.NewPCG(20472567, 1226)

	data := make([]byte, 400)
	for i := range data {
		data[i] = byte(src.Uint64() >> 32)
		if i > 0 && data[i] == data[i-1] {
			data[i]++
		}
	}

	stream := classic(t, wcase{opts: []lzma.Option{lzma.WithDictSize(lzma.MinDictSize)}}, data)

	const literalsOnly = 437

	if len(stream) != literalsOnly {
		t.Errorf(
			"the stream is %d bytes, not the %d of 400 literals: search for another seed",
			len(stream),
			literalsOnly,
		)
	}

	out, err := lzmaspec.Decode(stream)
	if err != nil || !bytes.Equal(out.Data, data) || out.Corrupted {
		t.Fatalf("the reference: %d bytes, %v, corrupted %v", len(out.Data), err, out.Corrupted)
	}

	r, err := lzma.NewReader(bytes.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}

	if got, err := io.ReadAll(r); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("the reader: %d bytes, %v", len(got), err)
	}
}

// failOnce serves the first n bytes of data and fails once with err; asked
// again, it says the input ended.
type failOnce struct {
	data []byte
	n    int
	err  error
}

func (f *failOnce) Read(p []byte) (int, error) {
	if f.n == 0 {
		err := f.err
		f.err = io.EOF

		return 0, err
	}

	k := copy(p[:min(len(p), f.n)], f.data)
	f.data, f.n = f.data[k:], f.n-k

	return k, nil
}

// TestContractInputEndingAtEveryByte: "An I/O error from the underlying
// reader matches neither sentinel and is returned as it is, with one
// exception: input that ends inside a stream, with io.EOF or an error
// wrapping it, is reported as io.ErrUnexpectedEOF. A reader keeps what it
// failed with: every later Read returns it." Held at every byte of a stream
// of each kind, so in a header, a chunk header, a compressed and a stored
// chunk, the data, the end marker and the range coder's last bytes:
//
//   - The input ending there is io.ErrUnexpectedEOF and neither sentinel, as
//     the reference has it, and what is read before it is the data's start.
//   - A source that hands over its last bytes together with io.EOF, which
//     io.Reader allows, is read to the same bytes as one that says io.EOF
//     after them.
//   - A source failing there is its own error, and stays the reader's
//     though the source would say io.EOF if asked again.
func TestContractInputEndingAtEveryByte(t *testing.T) {
	t.Parallel()

	text := []byte("the quick brown fox jumps over the lazy dog")

	// Bytes no encoder shortens, so the LZMA2 writer stores them.
	rng := rand.New(rand.NewPCG(3, 5))
	random := make([]byte, 3000)

	for i := range random {
		random[i] = byte(rng.Uint32())
	}

	// The text is too short for the writer to compress; four of it are not.
	repeated := bytes.Repeat(text, 4)

	// chunk is data as the writer's sequence, whose first chunk is of the
	// kind the control byte says.
	chunk := func(data []byte, control byte) []byte {
		sequence := lzma2Stream(t, data)
		if sequence[0] != control {
			t.Fatalf("the writer's first chunk of %d bytes is %#x, want %#x", len(data), sequence[0], control)
		}

		return sequence
	}

	errSource := errors.New("source failed")

	open1 := func(in io.Reader) (io.Reader, error) { return lzma.NewReader(in) }
	open2 := func(in io.Reader) (io.Reader, error) { return lzma.NewReader2(in, defaultDict) }
	ref1 := func(stream []byte) error {
		_, err := lzmaspec.Decode(stream)

		return err
	}
	ref2 := func(stream []byte) error {
		_, err := lzmaspec.DecodeLZMA2(stream, defaultDict)

		return err
	}

	// cutShort is whether err says the input ended inside the stream, and
	// nothing else.
	cutShort := func(err error) bool {
		return errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, lzma.ErrCorrupt) &&
			!errors.Is(err, lzma.ErrUnsupported)
	}

	for name, tc := range map[string]struct {
		stream, data []byte
		open         func(io.Reader) (io.Reader, error)
		ref          func([]byte) error
	}{
		"classic, a marker": {classic(t, wcase{}, text), text, open1, ref1},
		"classic, a size": {
			classic(t, wcase{opts: []lzma.Option{lzma.WithSize(int64(len(text)))}}, text), text, open1, ref1,
		},
		"classic, a size and a marker": {
			classic(t, wcase{opts: []lzma.Option{lzma.WithSize(int64(len(text))), lzma.WithEOSMarker()}}, text),
			text, open1, ref1,
		},
		"lzma2, a compressed chunk": {chunk(repeated, 0xE0), repeated, open2, ref2},
		"lzma2, a stored chunk":     {chunk(random, 0x01), random, open2, ref2},
	} {
		for n := range len(tc.stream) {
			cut := tc.stream[:n]

			if err := tc.ref(cut); !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("%s, cut at %d: the reference: %v, want io.ErrUnexpectedEOF", name, n, err)
			}

			var ended, with []byte

			r, err := tc.open(bytes.NewReader(cut))
			if err == nil {
				ended, err = io.ReadAll(r)
			}

			if !cutShort(err) || !bytes.HasPrefix(tc.data, ended) {
				t.Errorf("%s, cut at %d: %d bytes, %v; want the data's start and io.ErrUnexpectedEOF alone",
					name, n, len(ended), err)
			}

			r, err = tc.open(iotest.DataErrReader(bytes.NewReader(cut)))
			if err == nil {
				with, err = io.ReadAll(r)
			}

			if !cutShort(err) || !bytes.Equal(with, ended) {
				t.Errorf("%s, cut at %d, the last bytes along with io.EOF: %d bytes, %v; want the same %d and error",
					name, n, len(with), err, len(ended))
			}

			r, err = tc.open(&failOnce{data: tc.stream, n: n, err: errSource})
			if err == nil {
				_, err = io.ReadAll(r)

				if k, again := r.Read(make([]byte, 16)); k != 0 || !errors.Is(again, errSource) {
					t.Errorf("%s, the source failing at %d, a later Read: %d bytes, %v; want none and its error",
						name, n, k, again)
				}
			}

			if !errors.Is(err, errSource) || errors.Is(err, lzma.ErrCorrupt) || errors.Is(err, lzma.ErrUnsupported) {
				t.Errorf("%s, the source failing at %d: %v; want the source's error and neither sentinel", name, n, err)
			}
		}
	}
}

// TestContractReadReturnsAtAChunksEnd: Read "returns at the end of a chunk
// with what the chunk held, without waiting for the next one", whatever the
// chunk held, one byte as much as many, stored or compressed. The source
// here holds what the writer flushed and fails when asked for more, where a
// connection would block.
func TestContractReadReturnsAtAChunksEnd(t *testing.T) {
	t.Parallel()

	errNotYet := errors.New("the next chunk is not written yet")

	for name, held := range map[string][]byte{
		"a byte":             []byte("h"),
		"two bytes":          []byte("hi"),
		"a compressed chunk": bytes.Repeat([]byte("abc"), 100),
	} {
		var flushed bytes.Buffer

		w, err := lzma.NewWriter2(&flushed)
		if err != nil {
			t.Fatal(err)
		}

		if _, err = w.Write(held); err != nil {
			t.Fatal(err)
		}

		if err = w.Flush(); err != nil {
			t.Fatal(err)
		}

		r, err := lzma.NewReader2(&failAfter{data: flushed.Bytes(), n: flushed.Len(), err: errNotYet}, defaultDict)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}

		p := make([]byte, len(held)+16)

		if n, rerr := r.Read(p); rerr != nil || !bytes.Equal(p[:n], held) {
			t.Errorf("%s: Read: %q, %v; want the chunk and no error", name, p[:n], rerr)
		}
	}
}

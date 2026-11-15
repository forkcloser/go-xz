// Copyright 2026 Forkcloser. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The contract tests hold the package to the .xz file format specification
// through the reference container (reference_test.go), written from the
// specification and decoding its payload with the reference LZMA decoder,
// so nothing here is checked against the package itself:
//
//   - What the writer produces, the reference parses to the input, with
//     the check type, the block count and the sizes the configuration asked
//     for.
//   - What the reference writes, both readers decode to the input: every
//     check type, sizes present and absent, one block, several, empty
//     blocks, several streams with and without padding.
//   - What the reference refuses, both readers refuse, as ErrCorrupt,
//     ErrUnsupported or io.ErrUnexpectedEOF; what it parses, both decode to
//     the same bytes: every byte flipped, every byte zeroed and every
//     truncation of a corpus of files, and an index whose records lie
//     about a block under a recomputed CRC.
//   - The parallel reader's WriteTo delivers the same bytes and counts
//     them; a writer's failure, or a writer that accepts nothing, ends it.

package xz_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"math"
	"math/rand/v2"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/forkcloser/xz/v2"
	"github.com/forkcloser/xz/v2/lzma"
)

// cData is n bytes of text with a random run in the middle.
func cData(n int) []byte {
	rng := rand.New(rand.NewPCG(uint64(n), 5))
	text := []byte("Sphinx of black quartz, judge my vow. ")
	out := make([]byte, n)

	for i := range out {
		if i >= n/2 && i < n/2+n/8 {
			out[i] = byte(rng.Uint32())
		} else {
			out[i] = text[i%len(text)]
		}
	}

	return out
}

// lzma2Of is data as an LZMA2 chunk sequence from the lzma package, whose
// own contract holds that output to the reference decoder.
func lzma2Of(tb testing.TB, data []byte) []byte {
	tb.Helper()

	var buf bytes.Buffer

	w, err := lzma.NewWriter2(&buf)
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

// refBlocks cuts data into blocks of at most size bytes, one empty block
// for no data; a size of zero is one block holding all of it.
func refBlocks(tb testing.TB, data []byte, size int) []refBlock {
	tb.Helper()

	if size <= 0 {
		return []refBlock{{data: data, lzma2: lzma2Of(tb, data)}}
	}

	var blocks []refBlock

	for pos := 0; pos < len(data) || len(blocks) == 0; pos += size {
		end := min(pos+size, len(data))
		blocks = append(blocks, refBlock{data: data[pos:end], lzma2: lzma2Of(tb, data[pos:end])})
	}

	return blocks
}

// checks are the check types by name, as the writer takes them and as the
// stream flags carry them.
func checks() map[string]struct {
	check xz.Check
	flags byte
} {
	return map[string]struct {
		check xz.Check
		flags byte
	}{
		"none":   {xz.CheckNone, flagNone},
		"crc32":  {xz.CheckCRC32, flagCRC32},
		"crc64":  {xz.CheckCRC64, flagCRC64},
		"sha256": {xz.CheckSHA256, flagSHA256},
	}
}

// both decodes data through the sequential and the parallel reader and
// returns each verdict.
func both(data []byte) (sequential, parallel verdict) {
	r, err := xz.NewReader(bytes.NewReader(data))
	if err != nil {
		sequential = verdict{err: err}
	} else {
		out, rerr := io.ReadAll(r)
		sequential = verdict{data: out, err: rerr}
	}

	pr, err := xz.NewReader(bytes.NewReader(data), xz.WithWorkers(4))
	if err != nil {
		return sequential, verdict{err: err}
	}

	out, err := io.ReadAll(pr)
	parallel = verdict{data: out, err: err}

	_ = pr.Close()

	return sequential, parallel
}

// verdict is what a decoder made of a file: the bytes, or the failure.
type verdict struct {
	data []byte
	err  error
}

// TestContractWriter: the writer's files parse under the reference, with
// the check type asked for, the sizes in every block header, and as many
// blocks as the block size cuts.
func TestContractWriter(t *testing.T) {
	t.Parallel()

	for _, n := range []int{0, 1, 5000, 70000} {
		data := cData(n)

		for cname, check := range checks() {
			for _, blockSize := range []int64{0, 1024, 65536} {
				var buf bytes.Buffer

				opts := []xz.Option{xz.WithCheck(check.check)}
				if blockSize > 0 {
					opts = append(opts, xz.WithBlockSize(blockSize))
				}

				w, err := xz.NewWriter(&buf, opts...)
				if err != nil {
					t.Fatal(err)
				}

				if _, err = w.Write(data); err != nil {
					t.Fatal(err)
				}

				if err = w.Close(); err != nil {
					t.Fatal(err)
				}

				file := buf.Bytes()

				got, err := refParse(file)
				if err != nil || !bytes.Equal(got, data) {
					t.Fatalf("len %d, %s, block %d: the reference: %d bytes, %v", n, cname, blockSize, len(got), err)
				}

				if file[7] != check.flags {
					t.Fatalf("len %d, %s: the stream flags say check %#x", n, cname, file[7])
				}

				// A block begins with its first byte: none for no data,
				// one for any without a block size.
				want := min(n, 1)
				if blockSize > 0 {
					want = (n + int(blockSize) - 1) / int(blockSize)
				}

				if blocks := refCountBlocks(file); blocks != want {
					t.Fatalf("len %d, %s, block %d: %d blocks, want %d", n, cname, blockSize, blocks, want)
				}
			}
		}
	}
}

// TestContractWriterEmptyInput: "a writer given no data writes no block,
// the empty stream xz writes": the header, an index of no records and the
// footer, 32 bytes, which is the reference's stream of no blocks byte for
// byte, whatever the check, the block size and the workers, and whether
// nothing was written or only writes of nothing.
func TestContractWriterEmptyInput(t *testing.T) {
	t.Parallel()

	for cname, check := range checks() {
		want := refStream(refOptions{check: check.flags}, nil)
		if len(want) != 32 {
			t.Fatalf("%s: the reference's empty stream is %d bytes", cname, len(want))
		}

		for oname, opts := range map[string][]xz.Option{
			"no option":    nil,
			"a block size": {xz.WithBlockSize(1000)},
			"workers":      {xz.WithWorkers(2)},
		} {
			opts = append([]xz.Option{xz.WithCheck(check.check)}, opts...)

			for wname, pieces := range map[string][][]byte{
				"nothing written":   nil,
				"writes of nothing": {nil, {}, {}},
			} {
				if file := writeAll(t, opts, pieces...); !bytes.Equal(file, want) {
					t.Errorf("%s, %s, %s: %d bytes, not the reference's empty stream", cname, oname, wname, len(file))
				}
			}
		}
	}
}

// errNoRoom is what a countingWriter out of room fails with here.
var errNoRoom = errors.New("no room left")

// TestContractWriterCloseKeepsItsFailure: "a second Close writes nothing and
// returns an error matching ErrClosed, or the error the first one failed
// with". An underlying writer with room for the stream header and not for
// the index and the footer that end an empty stream fails Close wherever
// its room ends; a second Close and a Write return that failure, and
// nothing more reaches it.
func TestContractWriterCloseKeepsItsFailure(t *testing.T) {
	t.Parallel()

	for wname, workers := range map[string][]xz.Option{"no workers": nil, "workers": {xz.WithWorkers(2)}} {
		for room := 12; room < 32; room++ {
			out := &countingWriter{limit: room, fail: errNoRoom}

			w, err := xz.NewWriter(out, workers...)
			if err != nil {
				t.Fatalf("%s, room for %d bytes: NewWriter: %v", wname, room, err)
			}

			if err = w.Close(); !errors.Is(err, errNoRoom) {
				t.Fatalf("%s, room for %d bytes: Close: %v, want the writer's error", wname, room, err)
			}

			written := out.buf.Len()

			if err = w.Close(); !errors.Is(err, errNoRoom) {
				t.Errorf("%s, room for %d bytes: a second Close: %v, want the first one's failure", wname, room, err)
			}

			if n, werr := w.Write([]byte("more")); n != 0 || !errors.Is(werr, errNoRoom) {
				t.Errorf("%s, room for %d bytes: Write after the failed Close: %d, %v", wname, room, n, werr)
			}

			if out.buf.Len() != written {
				t.Errorf(
					"%s, room for %d bytes: %d bytes written after the failed Close",
					wname, room, out.buf.Len()-written,
				)
			}
		}
	}
}

// refCountBlocks counts a one-stream file's blocks by its index.
func refCountBlocks(file []byte) int {
	// The footer's backward size locates the index; its count follows the
	// indicator byte.
	backward := int(
		uint32(
			file[len(file)-8],
		) | uint32(
			file[len(file)-7],
		)<<8 | uint32(
			file[len(file)-6],
		)<<16 | uint32(
			file[len(file)-5],
		)<<24,
	)
	indexStart := len(file) - 12 - (backward+1)*4
	p := &refParser{data: file, pos: indexStart + 1}

	count, err := p.vli()
	if err != nil {
		return -1
	}

	return int(count)
}

// TestContractReaders: both readers decode every reference file to its
// data: every check type, sizes present and absent, one block, several
// and an empty one, several streams with and without padding.
func TestContractReaders(t *testing.T) {
	t.Parallel()

	data := cData(30000)

	for cname, check := range checks() {
		for _, sizes := range []bool{false, true} {
			o := refOptions{check: check.flags, sizes: sizes, dictCode: 35}

			files := map[string][]byte{
				"one block": refStream(o, refBlocks(t, data, 0)),
				"blocks":    refStream(o, refBlocks(t, data, 7000)),
				"empty":     refStream(o, refBlocks(t, []byte{}, 0)),
				"no blocks": refStream(o, nil),
				"streams": refFile(
					0,
					refStream(o, refBlocks(t, data[:10000], 0)),
					refStream(o, refBlocks(t, data[10000:], 3000)),
				),
				"padded": refFile(
					8,
					refStream(o, refBlocks(t, data[:10000], 0)),
					refStream(o, refBlocks(t, data[10000:], 0)),
				),
				"empty between": refFile(
					4,
					refStream(o, refBlocks(t, data[:10000], 0)),
					refStream(o, nil),
					refStream(o, refBlocks(t, data[10000:], 0)),
				),
			}

			for fname, file := range files {
				want, err := refParse(file)
				if err != nil {
					t.Fatalf("%s, sizes %v, %s: the reference refuses its own file: %v", cname, sizes, fname, err)
				}

				sequential, parallel := both(file)
				if sequential.err != nil || !bytes.Equal(sequential.data, want) {
					t.Fatalf(
						"%s, sizes %v, %s: sequential: %d bytes, %v",
						cname,
						sizes,
						fname,
						len(sequential.data),
						sequential.err,
					)
				}

				if parallel.err != nil || !bytes.Equal(parallel.data, want) {
					t.Fatalf(
						"%s, sizes %v, %s: parallel: %d bytes, %v",
						cname,
						sizes,
						fname,
						len(parallel.data),
						parallel.err,
					)
				}
			}
		}
	}
}

// classify requires a reader's verdict to follow the reference's: where
// the reference parses, the same bytes; where it refuses, a refusal that
// matches one of the package's sentinels.
func classify(t *testing.T, what string, ref, got verdict) {
	t.Helper()

	if ref.err == nil {
		if got.err != nil || !bytes.Equal(got.data, ref.data) {
			t.Fatalf(
				"%s: the reference parses %d bytes; the reader: %d bytes, %v",
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
	case errors.Is(got.err, xz.ErrCorrupt),
		errors.Is(got.err, xz.ErrUnsupported),
		errors.Is(got.err, io.ErrUnexpectedEOF):
	default:
		t.Fatalf("%s: the reader's error matches no sentinel: %v", what, got.err)
	}
}

// TestContractMalformed: on every byte flip, every byte zeroed and every
// truncation of a small corpus of reference files, both readers follow the
// reference's verdict. A zeroed byte is a size field claiming nothing where
// a flip only claims more.
func TestContractMalformed(t *testing.T) {
	t.Parallel()

	data := cData(600)

	files := map[string][]byte{
		"crc32 sizes":  refStream(refOptions{check: flagCRC32, sizes: true, dictCode: 20}, refBlocks(t, data, 250)),
		"crc64":        refStream(refOptions{check: flagCRC64, dictCode: 20}, refBlocks(t, data, 0)),
		"sha256 empty": refStream(refOptions{check: flagSHA256, sizes: true, dictCode: 20}, refBlocks(t, []byte{}, 0)),
		"no blocks":    refStream(refOptions{check: flagCRC32, sizes: true, dictCode: 20}, nil),
		"none streams": refFile(
			4,
			refStream(refOptions{check: flagNone, dictCode: 20}, refBlocks(t, data[:300], 0)),
			refStream(refOptions{check: flagNone, sizes: true, dictCode: 20}, refBlocks(t, data[300:], 0)),
		),
	}

	for fname, file := range files {
		bad := make([]byte, len(file))

		for i := range file {
			for _, mask := range []byte{0x01, 0x80, 0xff, file[i]} {
				copy(bad, file)
				bad[i] ^= mask

				ref, err := refParse(bad)
				sequential, parallel := both(bad)
				what := fname + ": byte " + strconv.Itoa(i) + " ^ " + strconv.Itoa(int(mask))
				classify(t, what+", sequential", verdict{ref, err}, sequential)
				classify(t, what+", parallel", verdict{ref, err}, parallel)
			}
		}

		for n := range file {
			ref, err := refParse(file[:n])
			sequential, parallel := both(file[:n])
			what := fname + ": cut at " + strconv.Itoa(n)
			classify(t, what+", sequential", verdict{ref, err}, sequential)
			classify(t, what+", parallel", verdict{ref, err}, parallel)
		}
	}
}

// rawBlock is data as one uncompressed LZMA2 chunk, so the block's size is
// the data's plus a fixed four: every residue of the unpadded size modulo
// four is one data length away.
func rawBlock(data []byte) refBlock {
	lzma2 := []byte{0x01, byte((len(data) - 1) >> 8), byte(len(data) - 1)}
	lzma2 = append(lzma2, data...)

	return refBlock{data: data, lzma2: append(lzma2, 0x00)}
}

// TestContractWriterRefusesBeforeWriting: an option out of range is the
// constructor's error, with or without workers, and the constructor has
// then written nothing, the dictionary and the properties LZMA2 cannot take
// included.
func TestContractWriterRefusesBeforeWriting(t *testing.T) {
	t.Parallel()

	for name, opt := range map[string]xz.Option{
		"a dictionary below the smallest": xz.WithDictSize(lzma.MinDictSize - 1),
		"no dictionary":                   xz.WithDictSize(0),
		"a negative dictionary":           xz.WithDictSize(-1),
		"lc and lp above four":            xz.WithProperties(3, 2, 2),
		"lc above eight":                  xz.WithProperties(9, 0, 2),
		"pb above four":                   xz.WithProperties(3, 0, 5),
		"a negative lc":                   xz.WithProperties(-1, 0, 2),
		"a negative lp":                   xz.WithProperties(3, -1, 2),
		"a negative pb":                   xz.WithProperties(3, 0, -1),
		"a negative block size":           xz.WithBlockSize(-1),
		"an unknown check":                xz.WithCheck(xz.Check(9)),
		"a delta distance of zero":        xz.WithFilters(xz.Filter{ID: xz.Delta}),
	} {
		for wname, workers := range map[string][]xz.Option{"no workers": nil, "workers": {xz.WithWorkers(2)}} {
			var out bytes.Buffer

			w, err := xz.NewWriter(&out, append([]xz.Option{opt}, workers...)...)
			if err == nil || w != nil {
				t.Errorf("%s, %s: NewWriter returned a writer and %v", name, wname, err)
			}

			if out.Len() != 0 {
				t.Errorf("%s, %s: %d bytes written before the refusal", name, wname, out.Len())
			}
		}
	}

	// The largest dictionary is taken and one byte more is not. With
	// workers no encoder is built before a block is, so neither allocates
	// it. An int holds these sizes where it is 64 bits wide, and the
	// conversion is of a variable so that this compiles where it is not.
	if largest := int64(lzma.MaxDictSize); math.MaxInt > lzma.MaxDictSize {
		var out bytes.Buffer

		if _, err := xz.NewWriter(&out, xz.WithDictSize(int(largest)), xz.WithWorkers(2)); err != nil {
			t.Errorf("the largest dictionary: %v", err)
		}

		out.Reset()

		w, err := xz.NewWriter(&out, xz.WithDictSize(int(largest+1)), xz.WithWorkers(2))
		if err == nil || w != nil || out.Len() != 0 {
			t.Errorf("a dictionary above the largest: a writer and %v, %d bytes written", err, out.Len())
		}
	}

	// The bounds themselves are taken.
	for name, opt := range map[string]xz.Option{
		"the smallest dictionary": xz.WithDictSize(lzma.MinDictSize),
		"lc four, lp none":        xz.WithProperties(4, 0, 4),
		"lc none, lp four":        xz.WithProperties(0, 4, 0),
	} {
		data := cData(3000)

		file := writeAll(t, []xz.Option{opt}, data)
		if got, err := refParse(file); err != nil || !bytes.Equal(got, data) {
			t.Errorf("%s: the reference reads %d bytes, %v", name, len(got), err)
		}
	}
}

// TestContractWriterKeepsItsFilters: the chain is the one given to
// WithFilters. A caller that reuses its slice afterwards changes nothing in
// a writer made from it: every block of the file has the filters the
// writer was made with.
func TestContractWriterKeepsItsFilters(t *testing.T) {
	t.Parallel()

	data := cData(3000)

	for wname, workers := range map[string][]xz.Option{"no workers": nil, "workers": {xz.WithWorkers(2)}} {
		opts := func(chain []xz.Filter) []xz.Option {
			return append([]xz.Option{xz.WithFilters(chain...), xz.WithBlockSize(1000)}, workers...)
		}

		want := writeAll(t, opts([]xz.Filter{{ID: xz.X86}}), data)

		chain := []xz.Filter{{ID: xz.X86}}

		var out bytes.Buffer

		w, err := xz.NewWriter(&out, opts(chain)...)
		if err != nil {
			t.Fatal(err)
		}

		if _, err = w.Write(data[:1500]); err != nil {
			t.Fatal(err)
		}

		chain[0] = xz.Filter{ID: xz.Delta, Distance: 9}

		if _, err = w.Write(data[1500:]); err != nil {
			t.Fatal(err)
		}

		if err = w.Close(); err != nil {
			t.Fatal(err)
		}

		if !bytes.Equal(out.Bytes(), want) {
			t.Errorf("%s: the file is not the one of the chain the writer was made with", wname)
		}
	}
}

// TestContractZeroOption: "the zero Option sets nothing": a reader and a
// writer given one are the reader and the writer given none.
func TestContractZeroOption(t *testing.T) {
	t.Parallel()

	data := cData(3000)

	want := writeAll(t, nil, data)
	if got := writeAll(t, []xz.Option{{}}, data); !bytes.Equal(got, want) {
		t.Error("a writer given the zero Option writes another file")
	}

	if got := writeAll(t, []xz.Option{{}, xz.WithBlockSize(1000)}, data); !bytes.Equal(got,
		writeAll(t, []xz.Option{xz.WithBlockSize(1000)}, data)) {
		t.Error("the zero Option before another changes what the other does")
	}

	r, err := xz.NewReader(bytes.NewReader(want), xz.Option{})
	if err != nil {
		t.Fatal(err)
	}

	if got, rerr := io.ReadAll(r); rerr != nil || !bytes.Equal(got, data) {
		t.Errorf("a reader given the zero Option: %d bytes, %v", len(got), rerr)
	}

	// Beside an option of the other constructor it is still that option
	// that is refused.
	if _, err = xz.NewReader(bytes.NewReader(want), xz.Option{}, xz.WithBlockSize(1)); err == nil {
		t.Error("NewReader accepted WithBlockSize beside the zero Option")
	}
}

// stalledInput serves the first bytes of a file and then answers every Read
// with no byte and no error, which io.Reader allows and asks callers to
// take as nothing having happened.
type stalledInput struct {
	data []byte
}

func (s *stalledInput) Read(p []byte) (int, error) {
	n := copy(p, s.data)
	s.data = s.data[n:]

	return n, nil
}

// TestContractInputMakingNoProgress: an input that goes on answering with
// no byte and no error ends the reader with io.ErrNoProgress, wherever in
// the file it stalls: in a stream header, a block header, a block, a check,
// the index or the footer, in a reader of every stream and in a reader of
// one.
func TestContractInputMakingNoProgress(t *testing.T) {
	t.Parallel()

	file := refStream(refOptions{check: flagCRC64, dictCode: 20}, refBlocks(t, cData(180), 60))

	for name, opts := range map[string][]xz.Option{
		"every stream":        nil,
		"one stream":          {xz.WithSingleStream()},
		"workers on a stream": {xz.WithWorkers(4)},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			done := make(chan struct{})

			go func() {
				defer close(done)

				for served := range file {
					err := func() error {
						r, err := xz.NewReader(&stalledInput{data: file[:served]}, opts...)
						if err != nil {
							return err
						}

						_, err = io.Copy(io.Discard, r)

						return err
					}()
					if !errors.Is(err, io.ErrNoProgress) {
						t.Errorf("stalled after %d of %d bytes: %v, want io.ErrNoProgress", served, len(file), err)
					}
				}
			}()

			select {
			case <-done:
			case <-time.After(30 * time.Second):
				t.Fatal("a reader is still asking")
			}
		})
	}
}

// TestContractAlignment: blocks whose unpadded size takes every residue
// modulo four, alone and as the last of several, with and without a check,
// decode under both readers; the one that ends exactly where the index
// starts is as valid as the one that needs three bytes of padding.
func TestContractAlignment(t *testing.T) {
	t.Parallel()

	data := cData(16)

	for cname, check := range checks() {
		for n := 1; n <= 8; n++ {
			o := refOptions{check: check.flags, dictCode: 20}
			files := map[string][]byte{
				"alone": refStream(o, []refBlock{rawBlock(data[:n])}),
				"last":  refStream(o, []refBlock{rawBlock(data[:5]), rawBlock(data[5 : 5+n])}),
			}

			for fname, file := range files {
				want, err := refParse(file)
				if err != nil {
					t.Fatalf("%s, %d bytes, %s: the reference refuses its own file: %v", cname, n, fname, err)
				}

				sequential, parallel := both(file)
				if sequential.err != nil || !bytes.Equal(sequential.data, want) {
					t.Fatalf("%s, %d bytes, %s: sequential: %v", cname, n, fname, sequential.err)
				}

				if parallel.err != nil || !bytes.Equal(parallel.data, want) {
					t.Fatalf("%s, %d bytes, %s: parallel: %v", cname, n, fname, parallel.err)
				}
			}
		}
	}
}

// TestContractLongPadding: stream padding longer than any read buffer,
// between streams and after the last, is still padding.
func TestContractLongPadding(t *testing.T) {
	t.Parallel()

	data := cData(2000)
	o := refOptions{check: flagCRC32, dictCode: 20}

	for _, padding := range []int{4, 40 << 10, 70 << 10} {
		file := refFile(padding, refStream(o, refBlocks(t, data[:1000], 0)), refStream(o, refBlocks(t, data[1000:], 0)))
		file = append(file, make([]byte, padding)...)

		want, err := refParse(file)
		if err != nil {
			t.Fatalf("padding %d: the reference refuses its own file: %v", padding, err)
		}

		sequential, parallel := both(file)
		if sequential.err != nil || !bytes.Equal(sequential.data, want) {
			t.Fatalf("padding %d: sequential: %v", padding, sequential.err)
		}

		if parallel.err != nil || !bytes.Equal(parallel.data, want) {
			t.Fatalf("padding %d: parallel: %v", padding, parallel.err)
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
}

func (m *miscounter) Write(p []byte) (int, error) {
	m.calls++
	// An empty write has no count to get wrong.
	if m.calls >= m.at && !m.hit && len(p) > 0 {
		m.hit = true

		return len(p) + m.delta, nil
	}

	return len(p), nil
}

// TestContractWriterHoldsItsWriterToItsCount: the stream goes out in many
// writes and the writer counts on each having gone out whole. An underlying
// writer that takes less than it was given without an error, at any one of
// its calls, is io.ErrShortWrite from the Write or Close that meets it, or
// with workers a later one; one that reports more than it was given is an
// error too. Neither is a file reported as written, and neither is a panic.
func TestContractWriterHoldsItsWriterToItsCount(t *testing.T) {
	t.Parallel()

	data := cData(3000)

	for wname, workers := range map[string][]xz.Option{"no workers": nil, "workers": {xz.WithWorkers(2)}} {
		for _, delta := range []int{-1, 3} {
			var calls int

			for at := 1; ; at++ {
				m := &miscounter{at: at, delta: delta}

				err := func() error {
					w, err := xz.NewWriter(m, append([]xz.Option{xz.WithBlockSize(1000)}, workers...)...)
					if err != nil {
						return err
					}

					if _, err = w.Write(data); err != nil {
						return err
					}

					return w.Close()
				}()

				if !m.hit {
					// The file is written in fewer calls than that.
					calls = at - 1

					break
				}

				if delta < 0 && !errors.Is(err, io.ErrShortWrite) {
					t.Errorf("%s, call %d a byte short: %v, want io.ErrShortWrite", wname, at, err)
				}

				if delta > 0 && err == nil {
					t.Errorf("%s, call %d counting %d too many: no error", wname, at, delta)
				}
			}

			if calls < 5 {
				t.Errorf("%s: the file went out in %d writes, too few to say anything", wname, calls)
			}
		}
	}
}

// countingWriter accepts everything up to its limit, then behaves as its
// mode says: accepts nothing without an error, or fails.
type countingWriter struct {
	buf   bytes.Buffer
	limit int
	fail  error
	stuck int // calls that took nothing
}

func (w *countingWriter) Write(p []byte) (int, error) {
	if w.buf.Len() >= w.limit {
		w.stuck++

		return 0, w.fail
	}

	k := min(len(p), w.limit-w.buf.Len())
	w.buf.Write(p[:k])

	if k < len(p) {
		return k, w.fail
	}

	return k, nil
}

// TestContractWriteTo: the parallel reader's WriteTo delivers the
// reference's bytes and counts them; after it, a Read is at the end and a
// second WriteTo writes nothing and succeeds, as io.Copy expects of a drained
// reader. A writer that fails ends it with that error and the count of
// bytes the writer took; one that takes nothing without an error ends it
// with io.ErrShortWrite on the second call, not after a spin.
func TestContractWriteTo(t *testing.T) {
	t.Parallel()

	data := cData(40000)
	file := refStream(refOptions{check: flagCRC64, dictCode: 20}, refBlocks(t, data, 9000))

	want, err := refParse(file)
	if err != nil {
		t.Fatal(err)
	}

	errSink := errors.New("sink")

	for _, tc := range []struct {
		name  string
		limit int
		fail  error
		want  error
	}{
		{"whole", len(want), nil, nil},
		{"failing", 12345, errSink, errSink},
		{"stuck", 12345, nil, io.ErrShortWrite},
	} {
		pr, err := xz.NewReader(bytes.NewReader(file), xz.WithWorkers(4))
		if err != nil {
			t.Fatal(err)
		}

		w := &countingWriter{limit: tc.limit, fail: tc.fail}

		n, err := pr.WriteTo(w)
		if !errors.Is(err, tc.want) || n != int64(w.buf.Len()) || !bytes.HasPrefix(want, w.buf.Bytes()) {
			t.Fatalf("%s: WriteTo: %d, %v; the writer took %d bytes", tc.name, n, err, w.buf.Len())
		}

		if tc.want == nil {
			if k, err := pr.Read(make([]byte, 1)); k != 0 || !errors.Is(err, io.EOF) {
				t.Fatalf("%s: after WriteTo, Read: %d, %v", tc.name, k, err)
			}

			if n, err := pr.WriteTo(w); n != 0 || err != nil {
				t.Fatalf("%s: a second WriteTo: %d, %v", tc.name, n, err)
			}
		}

		if errors.Is(tc.want, io.ErrShortWrite) && w.stuck > 2 {
			t.Fatalf("%s: the stuck writer was asked %d times after taking nothing", tc.name, w.stuck)
		}

		_ = pr.Close()
	}
}

// TestContractReservedCheck: "input this package cannot handle matches
// ErrUnsupported". The twelve check IDs the format reserves, in a stream
// that is otherwise whole and under valid CRCs, are that: the stream is
// well formed. A bit set in the half of the byte the format keeps for
// itself is ErrCorrupt, and so is a footer naming another check than the
// header, a reserved one included. ValidHeader, which "checks whether data
// is a correct xz file header", follows: a header with a reserved check is
// one, a header with a reserved bit is not.
func TestContractReservedCheck(t *testing.T) {
	t.Parallel()

	for id := range byte(16) {
		switch id {
		case flagNone, flagCRC32, flagCRC64, flagSHA256:
			continue
		}

		file := refStream(refOptions{check: id, dictCode: 20}, nil)

		sequential, parallel := both(file)
		for name, got := range map[string]verdict{"sequential": sequential, "parallel": parallel} {
			if !errors.Is(got.err, xz.ErrUnsupported) || errors.Is(got.err, xz.ErrCorrupt) {
				t.Errorf("check %#x, %s: %v, want an error matching ErrUnsupported alone", id, name, got.err)
			}
		}

		if !xz.ValidHeader(file[:xz.HeaderLen]) {
			t.Errorf("check %#x: ValidHeader refuses a correct header", id)
		}
	}

	for _, flags := range []byte{0x10, 0x14, 0x80, 0xff} {
		file := refStream(refOptions{check: flags, dictCode: 20}, nil)

		sequential, parallel := both(file)
		for name, got := range map[string]verdict{"sequential": sequential, "parallel": parallel} {
			if !errors.Is(got.err, xz.ErrCorrupt) {
				t.Errorf("stream flags %#x, %s: %v, want an error matching ErrCorrupt", flags, name, got.err)
			}
		}

		if xz.ValidHeader(file[:xz.HeaderLen]) {
			t.Errorf("stream flags %#x: ValidHeader accepts a header with a reserved bit", flags)
		}
	}

	// The footer is its CRC-32, the backward size, the two bytes of the
	// stream flags and the magic: a reserved check in it, under a CRC
	// recomputed, in a stream whose header says CRC-32.
	file := refStream(refOptions{check: flagCRC32, dictCode: 20}, refBlocks(t, cData(300), 0))
	footer := file[len(file)-12:]
	footer[9] = 0x02
	binary.LittleEndian.PutUint32(footer, crc32.ChecksumIEEE(footer[4:10]))

	sequential, parallel := both(file)
	for name, got := range map[string]verdict{"sequential": sequential, "parallel": parallel} {
		if !errors.Is(got.err, xz.ErrCorrupt) {
			t.Errorf("a footer naming another check, %s: %v, want an error matching ErrCorrupt", name, got.err)
		}
	}
}

// seeker is a file whose nth Seek fails.
type seeker struct {
	*bytes.Reader

	calls, failAt int
}

var errSeek = errors.New("the seek failed")

func (s *seeker) Seek(offset int64, whence int) (int64, error) {
	s.calls++
	if s.calls == s.failAt {
		return 0, errSeek
	}

	return s.Reader.Seek(offset, whence)
}

// TestContractSeekFailure: "an I/O error from the underlying reader is
// passed through untouched". A reader with workers asks a seekable input
// for its position and its size, then puts the position back. An input that
// cannot say either is read in order, from where it stands; one that cannot
// be put back has lost its position, and that is its error, not a file cut
// short.
func TestContractSeekFailure(t *testing.T) {
	t.Parallel()

	data := cData(5000)
	file := refStream(refOptions{check: flagCRC64, dictCode: 20}, refBlocks(t, data, 1000))

	for failAt := 1; failAt <= 2; failAt++ {
		r, err := xz.NewReader(&seeker{Reader: bytes.NewReader(file), failAt: failAt}, xz.WithWorkers(2))
		if err != nil {
			t.Fatalf("Seek %d fails: NewReader: %v", failAt, err)
		}

		if got, rerr := io.ReadAll(r); rerr != nil || !bytes.Equal(got, data) {
			t.Errorf("Seek %d fails: %d bytes, %v", failAt, len(got), rerr)
		}
	}

	//nolint:errorlint // the same error, untouched
	if _, err := xz.NewReader(&seeker{Reader: bytes.NewReader(file), failAt: 3}, xz.WithWorkers(2)); err != errSeek {
		t.Errorf("the Seek that puts the position back fails: %v, want the input's error", err)
	}
}

// lyingIndex returns file with one record of its one-stream index altered
// by delta on the first byte of the field (0 the unpadded size, 1 the
// uncompressed size) and the index CRC-32 recomputed, so only the records
// themselves disagree with the blocks. The footer's CRC covers the
// backward size and the flags, not the index, and stands.
func lyingIndex(file []byte, record, field, delta int) []byte {
	out := bytes.Clone(file)
	backward := int(binary.LittleEndian.Uint32(out[len(out)-8:]))
	start := len(out) - 12 - (backward+1)*4
	index := out[start : len(out)-12]

	p := &refParser{data: index, pos: 1}

	if _, err := p.vli(); err != nil {
		panic(err)
	}

	for i := 0; i <= record; i++ {
		for f := range 2 {
			at := p.pos

			if _, err := p.vli(); err != nil {
				panic(err)
			}

			if i == record && f == field {
				low := int(index[at]&0x7f) + delta
				if low < 0 || low > 0x7f {
					panic("the delta leaves the field's first byte")
				}

				index[at] = index[at]&0x80 | byte(low)
			}
		}
	}

	binary.LittleEndian.PutUint32(index[len(index)-4:], crc32.ChecksumIEEE(index[:len(index)-4]))

	return out
}

// failing serves n bytes of a file, then fails with err.
type failing struct {
	file []byte
	n    int
	err  error
}

func (f *failing) Read(p []byte) (int, error) {
	if f.n == 0 {
		return 0, f.err
	}

	k := copy(p[:min(len(p), f.n)], f.file)
	f.file, f.n = f.file[k:], f.n-k

	return k, nil
}

// TestContractSourceErrors: an error of the source "is returned as it is"
// and matches no sentinel, "except that input ending inside a stream, with
// io.EOF or an error wrapping it, is io.ErrUnexpectedEOF".
func TestContractSourceErrors(t *testing.T) {
	t.Parallel()

	data := cData(5000)
	file := refStream(refOptions{check: flagCRC64, dictCode: 20}, refBlocks(t, data, 1000))
	errSource := errors.New("source failed")

	for name, tc := range map[string]struct {
		cause, want error
		asItIs      bool
	}{
		"an error":        {errSource, errSource, true},
		"a wrapped error": {fmt.Errorf("read: %w", errSource), errSource, true},
		"io.EOF":          {io.EOF, io.ErrUnexpectedEOF, false},
		"wrapping io.EOF": {fmt.Errorf("gone: %w", io.EOF), io.ErrUnexpectedEOF, false},
	} {
		r, err := xz.NewReader(&failing{file: file, n: len(file) / 2, err: tc.cause})
		if err != nil {
			t.Fatalf("%s: opening: %v", name, err)
		}

		_, err = io.ReadAll(r)
		if !errors.Is(err, tc.want) || (tc.asItIs && !errors.Is(err, tc.cause)) {
			t.Errorf("%s: %v, want %v", name, err, tc.want)
		}

		for _, sentinel := range []error{xz.ErrCorrupt, xz.ErrUnsupported, xz.ErrClosed} {
			if errors.Is(err, sentinel) {
				t.Errorf("%s: %v matches %v", name, err, sentinel)
			}
		}
	}
}

// TestContractDictSizeStated: "every block header states the size, rounded
// up to the next one the format has a code for: a power of two, or one and
// a half times one". The code is the fifth byte of a block header whose
// chain is LZMA2 alone, after the twelve of the stream header.
func TestContractDictSizeStated(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		size int
		code byte
	}{
		{"4 MiB is a power of two", 4 << 20, 20},
		{"a byte above 4 MiB is 6 MiB", 4<<20 + 1, 21},
		{"5 MiB is 6 MiB", 5 << 20, 21},
		{"6 MiB is one and a half times 4 MiB", 6 << 20, 21},
		{"a byte above 6 MiB is 8 MiB", 6<<20 + 1, 22},
	} {
		var out bytes.Buffer

		w, err := xz.NewWriter(&out, xz.WithDictSize(tc.size))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}

		if _, err = w.Write([]byte("x")); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}

		if err = w.Close(); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}

		if got := out.Bytes()[12+4]; got != tc.code {
			t.Errorf("%s: the block header states code %d, want %d", tc.name, got, tc.code)
		}
	}
}

// holed is a file that is whole until armed, and then serves nothing of
// thirty bytes of itself: a read that reaches them stops short there.
type holed struct {
	*bytes.Reader

	hole  int64
	armed bool
}

func (h *holed) ReadAt(p []byte, off int64) (int, error) {
	if !h.armed || off+int64(len(p)) <= h.hole || off >= h.hole+30 {
		return h.Reader.ReadAt(p, off)
	}

	n, _ := h.Reader.ReadAt(p[:max(0, h.hole-off)], off)

	return n, io.EOF
}

// TestContractSourceServesLess: with workers, a source that stops serving
// part of a block after the file was opened whole is io.ErrUnexpectedEOF,
// "a stream cut short": the index is right and the bytes are not there,
// which is the transport's doing and not the file's.
func TestContractSourceServesLess(t *testing.T) {
	t.Parallel()

	data := cData(5000)
	file := refStream(refOptions{check: flagCRC64, dictCode: 20}, refBlocks(t, data, 1000))

	// Past the stream header and the first block's header, inside its data.
	in := &holed{Reader: bytes.NewReader(file), hole: 40}

	r, err := xz.NewReader(in, xz.WithWorkers(2))
	if err != nil {
		t.Fatal(err)
	}

	in.armed = true

	_, err = io.ReadAll(r)
	if !errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, xz.ErrCorrupt) {
		t.Errorf("%v, want io.ErrUnexpectedEOF and no match of ErrCorrupt", err)
	}
}

// TestContractLyingIndex: an index whose record disagrees with the block it
// describes, under a valid index CRC, is the file a hostile writer makes;
// the reference refuses it and so do both readers, whether the lie is the
// unpadded size, by which the parallel reader locates the blocks, or the
// uncompressed size, in the first record or the last, and whether it says
// more than the block holds or less. "Sizes that disagree" are ErrCorrupt:
// the file is all there, so neither reader may call it cut short.
func TestContractLyingIndex(t *testing.T) {
	t.Parallel()

	data := cData(600)

	for cname, check := range checks() {
		for _, sizes := range []bool{false, true} {
			file := refStream(refOptions{check: check.flags, sizes: sizes, dictCode: 20}, refBlocks(t, data, 250))

			if _, err := refParse(file); err != nil {
				t.Fatal(err)
			}

			for record := range 3 {
				for _, lie := range []struct {
					name         string
					field, delta int
				}{
					{"unpadded +1", 0, 1},
					{"unpadded -4", 0, -4},
					{"unpadded +4", 0, 4},
					{"uncompressed +1", 1, 1},
					{"uncompressed -1", 1, -1},
				} {
					bad := lyingIndex(file, record, lie.field, lie.delta)
					what := cname + ", sizes " + strconv.FormatBool(sizes) +
						": record " + strconv.Itoa(record) + " " + lie.name

					if _, err := refParse(bad); err == nil {
						t.Fatalf("%s: the reference accepts the lie", what)
					}

					sequential, parallel := both(bad)

					if !errors.Is(sequential.err, xz.ErrCorrupt) {
						t.Errorf("%s, sequential: %v, want an error matching ErrCorrupt", what, sequential.err)
					}

					if !errors.Is(parallel.err, xz.ErrCorrupt) {
						t.Errorf("%s, parallel: %v, want an error matching ErrCorrupt", what, parallel.err)
					}
				}
			}

			// Four bytes taken from one block and given to another: the
			// sum the parallel reader holds the stream to stands, and only
			// the blocks tell, the first by running out of its section.
			for _, move := range [][2]int{{0, 1}, {1, 2}, {2, 0}} {
				bad := lyingIndex(lyingIndex(file, move[0], 0, -4), move[1], 0, 4)
				what := cname + ", sizes " + strconv.FormatBool(sizes) + ": four bytes moved from record " +
					strconv.Itoa(move[0]) + " to " + strconv.Itoa(move[1])

				if _, err := refParse(bad); err == nil {
					t.Fatalf("%s: the reference accepts the lie", what)
				}

				sequential, parallel := both(bad)

				if !errors.Is(sequential.err, xz.ErrCorrupt) {
					t.Errorf("%s, sequential: %v, want an error matching ErrCorrupt", what, sequential.err)
				}

				if !errors.Is(parallel.err, xz.ErrCorrupt) {
					t.Errorf("%s, parallel: %v, want an error matching ErrCorrupt", what, parallel.err)
				}
			}
		}
	}
}

// readers are the three ways a file is read: in order, with workers on an
// input that cannot be read by offset (so in order again), and with workers
// through the indexes.
func readers() map[string]func(file []byte) (*xz.Reader, error) {
	return map[string]func(file []byte) (*xz.Reader, error){
		"in order": func(file []byte) (*xz.Reader, error) {
			return xz.NewReader(bytes.NewReader(file))
		},
		"workers on a stream": func(file []byte) (*xz.Reader, error) {
			return xz.NewReader(bytes.NewBuffer(file), xz.WithWorkers(4))
		},
		"workers": func(file []byte) (*xz.Reader, error) {
			return xz.NewReader(bytes.NewReader(file), xz.WithWorkers(4))
		},
	}
}

// TestContractReaderKeepsItsFirstError: "its first error is the one every
// later call returns". After a Read has failed, the next ones return that
// error and no byte, and so does WriteTo, where the bytes after the fault
// are a whole valid file: a block header with a wrong CRC before a valid
// block, a wrong check before the right one, a byte after the stream of a
// single-stream reader, an index with a wrong CRC.
func TestContractReaderKeepsItsFirstError(t *testing.T) {
	t.Parallel()

	data := cData(1 << 10)
	o := refOptions{check: flagCRC32, dictCode: 20}
	block := refBlocks(t, data, 0)
	good := refStream(o, block)

	header := refBlockHeader(o, block[0])
	header[len(header)-1] ^= 0xff

	// The index is the last thing before the 12 bytes of the footer, its
	// CRC-32 the last four of it; the block's check, four bytes, ends where
	// the index starts.
	indexSize := (int(binary.LittleEndian.Uint32(good[len(good)-8:])) + 1) * 4
	checkEnd := len(good) - 12 - indexSize

	wrongCheck := slices.Clone(good[:checkEnd])
	wrongCheck[checkEnd-1] ^= 0xff
	wrongCheck = append(append(wrongCheck, good[checkEnd-4:checkEnd]...), good[checkEnd:]...)

	wrongIndex := slices.Clone(good)
	wrongIndex[len(good)-13] ^= 0xff

	for _, tc := range []struct {
		name string
		file []byte
		opts []xz.Option
	}{
		{"a block header with a wrong CRC, then the valid file", slices.Concat(good[:12], header, good[12:]), nil},
		{"a wrong check, then the right one", wrongCheck, nil},
		{"a byte after the stream", append(slices.Clone(good), 1), []xz.Option{xz.WithSingleStream()}},
		{"an index with a wrong CRC", wrongIndex, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if _, err := refParse(tc.file); err == nil && len(tc.opts) == 0 {
				t.Fatal("the reference accepts the file")
			}

			r, err := xz.NewReader(bytes.NewReader(tc.file), tc.opts...)
			if err != nil {
				t.Fatal(err)
			}

			var first error

			buf := make([]byte, 4<<10)
			for first == nil {
				_, first = r.Read(buf)
			}

			if !errors.Is(first, xz.ErrCorrupt) {
				t.Fatalf("the first error is %v, want one matching ErrCorrupt", first)
			}

			for range 3 {
				//nolint:errorlint // the same error, not one like it
				if n, rerr := r.Read(buf); n != 0 || rerr != first {
					t.Fatalf("Read after %q: %d bytes, %v", first, n, rerr)
				}
			}

			//nolint:errorlint // the same error, not one like it
			if n, werr := r.WriteTo(io.Discard); n != 0 || werr != first {
				t.Errorf("WriteTo after %q: %d bytes, %v", first, n, werr)
			}
		})
	}
}

// TestContractReaderEndIsFinal: after io.EOF every Read returns io.EOF and
// no byte, an empty one included, however the file is read.
func TestContractReaderEndIsFinal(t *testing.T) {
	t.Parallel()

	data := cData(5000)
	file := refStream(refOptions{check: flagCRC64, dictCode: 20}, refBlocks(t, data, 1000))

	for name, open := range readers() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			r, err := open(file)
			if err != nil {
				t.Fatal(err)
			}

			if got, rerr := io.ReadAll(r); rerr != nil || !bytes.Equal(got, data) {
				t.Fatalf("%d bytes, %v", len(got), rerr)
			}

			for _, p := range [][]byte{nil, {}, make([]byte, 1), make([]byte, 100)} {
				//nolint:errorlint // io.EOF itself: callers compare it with ==
				if n, rerr := r.Read(p); n != 0 || rerr != io.EOF {
					t.Errorf("Read of %d bytes after the end: %d, %v", len(p), n, rerr)
				}
			}
		})
	}
}

// TestContractReaderClose: "Close closes the reader: Read and WriteTo then
// return an error matching ErrClosed, unless the reader had already ended
// or failed, which it keeps reporting", whatever the input and the workers;
// and Close returns nil, a second time too.
func TestContractReaderClose(t *testing.T) {
	t.Parallel()

	data := cData(5000)
	file := refStream(refOptions{check: flagCRC64, dictCode: 20}, refBlocks(t, data, 1000))
	cut := file[:len(file)-20]

	for name, open := range readers() {
		t.Run(name+", closed before the end", func(t *testing.T) {
			t.Parallel()

			r, err := open(file)
			if err != nil {
				t.Fatal(err)
			}

			if _, err = io.ReadFull(r, make([]byte, 1500)); err != nil {
				t.Fatal(err)
			}

			for range 2 {
				if cerr := r.Close(); cerr != nil {
					t.Fatalf("Close: %v", cerr)
				}
			}

			if n, rerr := r.Read(make([]byte, 100)); n != 0 || !errors.Is(rerr, xz.ErrClosed) {
				t.Errorf("Read after Close: %d bytes, %v", n, rerr)
			}

			if n, werr := r.WriteTo(io.Discard); n != 0 || !errors.Is(werr, xz.ErrClosed) {
				t.Errorf("WriteTo after Close: %d bytes, %v", n, werr)
			}
		})

		t.Run(name+", closed at the end", func(t *testing.T) {
			t.Parallel()

			r, err := open(file)
			if err != nil {
				t.Fatal(err)
			}

			if _, err = io.ReadAll(r); err != nil {
				t.Fatal(err)
			}

			if cerr := r.Close(); cerr != nil {
				t.Fatalf("Close: %v", cerr)
			}

			//nolint:errorlint // io.EOF itself: callers compare it with ==
			if n, rerr := r.Read(make([]byte, 100)); n != 0 || rerr != io.EOF {
				t.Errorf("Read after the end and Close: %d bytes, %v", n, rerr)
			}
		})

		t.Run(name+", closed after a failure", func(t *testing.T) {
			t.Parallel()

			r, err := open(cut)
			if err != nil {
				// The file is refused when it is opened: no reader to close.
				return
			}

			_, first := io.ReadAll(r)
			if first == nil {
				t.Fatal("a file cut short was read to its end")
			}

			if cerr := r.Close(); cerr != nil {
				t.Fatalf("Close: %v", cerr)
			}

			//nolint:errorlint // the same error, not one like it
			if n, rerr := r.Read(make([]byte, 100)); n != 0 || rerr != first {
				t.Errorf("Read after %q and Close: %d bytes, %v", first, n, rerr)
			}
		})
	}
}

// closingInput serves the first half of a file, then closes the reader that
// reads it, from inside its own Read, and fails.
type closingInput struct {
	file []byte
	pos  int
	r    *xz.Reader
}

var errInputGone = errors.New("the input is gone")

func (in *closingInput) Read(p []byte) (int, error) {
	if in.pos >= len(in.file)/2 {
		_ = in.r.Close()

		return 0, errInputGone
	}

	n := copy(p, in.file[in.pos:len(in.file)/2])
	in.pos += n

	return n, nil
}

// TestContractReaderCloseDuringRead: a Close that comes while a Read is at
// work is the reader's first error, whatever that Read then meets: the Read
// returns what it met, the input's own error, and every later one matches
// ErrClosed.
func TestContractReaderCloseDuringRead(t *testing.T) {
	t.Parallel()

	file := refStream(refOptions{check: flagCRC64, dictCode: 20}, refBlocks(t, cData(5000), 1000))
	in := &closingInput{file: file}

	r, err := xz.NewReader(in)
	if err != nil {
		t.Fatal(err)
	}

	in.r = r

	//nolint:errorlint // the input's error, untouched
	if _, rerr := io.ReadAll(r); rerr != errInputGone {
		t.Fatalf("the Read the input failed under: %v, want the input's error", rerr)
	}

	if n, rerr := r.Read(make([]byte, 100)); n != 0 || !errors.Is(rerr, xz.ErrClosed) {
		t.Errorf("the next Read: %d bytes, %v, want an error matching ErrClosed", n, rerr)
	}
}

// closingWriter closes the reader from another goroutine during its first
// Write, waits for that Close to return, and goes on accepting.
type closingWriter struct {
	r      *xz.Reader
	closed bool
}

func (w *closingWriter) Write(p []byte) (int, error) {
	if !w.closed {
		w.closed = true

		done := make(chan struct{})

		go func() {
			_ = w.r.Close()

			close(done)
		}()

		<-done
	}

	return len(p), nil
}

// TestContractCloseDuringWriteTo: a copy cut by a Close from another
// goroutine is never reported as a whole one. With 300 blocks and four
// workers the Close lands long before the end, so WriteTo and io.Copy
// return an error matching ErrClosed, every time.
func TestContractCloseDuringWriteTo(t *testing.T) {
	t.Parallel()

	data := cData(300 * 2000)
	file := refStream(refOptions{check: flagCRC32, dictCode: 20}, refBlocks(t, data, 2000))

	for name, cp := range map[string]func(w io.Writer, r *xz.Reader) (int64, error){
		"WriteTo": func(w io.Writer, r *xz.Reader) (int64, error) { return r.WriteTo(w) },
		"io.Copy": func(w io.Writer, r *xz.Reader) (int64, error) { return io.Copy(w, r) },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			for trial := range 100 {
				r, err := xz.NewReader(bytes.NewReader(file), xz.WithWorkers(4))
				if err != nil {
					t.Fatal(err)
				}

				n, err := cp(&closingWriter{r: r}, r)
				if !errors.Is(err, xz.ErrClosed) {
					t.Fatalf("trial %d: %d of %d bytes copied, %v", trial, n, len(data), err)
				}
			}
		})
	}
}

// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lzma_test

import (
	"bytes"
	"io"
	"math/rand/v2"
	"testing"

	"github.com/forkcloser/xz/v2/internal/lzmaspec"
	"github.com/forkcloser/xz/v2/lzma"
)

// TestWriter2SmallDictIncompressible verifies that a writer with a small
// dictionary can encode incompressible data. Such data fills the 64 KiB
// compressed-chunk limit before the chunk is cut, so the uncompressed form of
// the chunk would be smaller — but a dictionary below that size can no longer
// replay the chunk's input, and choosing the uncompressed form made Write and
// Close fail with ErrSizeExceeded.
func TestWriter2SmallDictIncompressible(t *testing.T) {
	t.Parallel()

	rnd := rand.NewChaCha8([32]byte{13})

	for _, dictCap := range []int{lzma.MinDictSize, 8192, 16384, 1 << 16} {
		for _, size := range []int{5000, 10000, 100000} {
			data := make([]byte, size)
			_, _ = rnd.Read(data)

			var buf bytes.Buffer

			w, err := lzma.NewWriter2(&buf, lzma.WithDictSize(dictCap))
			if err != nil {
				t.Fatalf("NewWriter2 error %s", err)
			}

			if _, err = w.Write(data); err != nil {
				t.Fatalf("dictCap=%d size=%d: Write error %s",
					dictCap, size, err)
			}

			if err = w.Close(); err != nil {
				t.Fatalf("dictCap=%d size=%d: Close error %s",
					dictCap, size, err)
			}

			r, err := lzma.NewReader2(&buf, dictCap)
			if err != nil {
				t.Fatalf("NewReader2 error %s", err)
			}

			out := new(bytes.Buffer)
			if _, err = io.Copy(out, r); err != nil {
				t.Fatalf("dictCap=%d size=%d: decompress error %s",
					dictCap, size, err)
			}

			if !bytes.Equal(out.Bytes(), data) {
				t.Fatalf("dictCap=%d size=%d: decompressed data differs "+
					"from original", dictCap, size)
			}
		}
	}
}

// costlyMatchInput is an input whose last match costs the range encoder
// thirteen bytes, placed so that it starts with the room for sixteen or
// seventeen left under an LZMA2 chunk's compressed limit when run is 15601
// or 15600.
//
// Sixty-four random bytes come first and are copied from at the very end,
// eighteen of them, more than a megabyte of zeros away: a match after
// literals, at a far distance, of a length in the long range. Between the
// two the encoder is taught to expect none of that. Ten times, two hundred
// fresh random segments are followed by a copy of each, cut after the same
// length: the first eight lengths walk the long lengths' tree from its
// deepest bit to its first, each against the bit that eighteen takes, the
// ninth sets the second choice of a length against the long range and the
// tenth the first. Each copy has three bytes changed and then five more of
// its segment, a repeat at the last distance three literals after a match,
// which sets "a repeat" against the match to come in that state. All of it
// is copied from nearby. Then run random bytes: literals, in that state.
func costlyMatchInput(run int) []byte {
	const (
		times = 200
		tail  = 8
	)

	rng := rand.NewPCG(1, 16)
	random := func(n int) []byte {
		p := make([]byte, n)
		for i := range p {
			p[i] = byte(rng.Uint64() >> 32)
		}

		return p
	}

	far := random(64)
	out := append(bytes.Clone(far), make([]byte, 1100000)...)

	// The lengths, and what follows each copy: the last stage keeps its
	// distances sixteen apart, so that their low four bits are set against
	// the match's too.
	for _, stage := range [][2]int{
		{19, 0}, {20, 0}, {22, 0}, {26, 0}, {34, 0}, {50, 0}, {82, 0}, {146, 0}, {10, 0}, {5, 15},
	} {
		n, pad := stage[0], stage[1]
		src := random(times * (n + tail))
		out = append(out, src...)

		for i := range times {
			seg := src[i*(n+tail) : (i+1)*(n+tail)]
			out = append(out, seg[:n]...)
			out = append(out, seg[n]^0x55, seg[n+1]^0x55, seg[n+2]^0x55)
			out = append(out, seg[n+3:]...)

			next := byte(0)
			if i+1 < times {
				next = src[(i+1)*(n+tail)]
			}

			out = append(out, next^0x55)
			out = append(out, random(pad)...)
		}
	}

	out = append(out, random(run)...)
	out = append(out, far[8:26]...)
	out = append(out, far[26]^0x55)

	return append(out, random(16)...)
}

// TestWriter2CostlyMatchAtTheChunkLimit: the chunk limit "is internal to the
// encoder: Writer2 turns it into a chunk boundary ... so it does not reach
// a caller". It did: the encoder started an operation with room for
// sixteen bytes, held to be the most one takes, and a match that took
// thirteen left fewer than the five the range coder needs to close the
// chunk, so Close failed with "lzma: chunk limit reached" on input that is
// as valid as any. The input is built for this encoder byte for byte, and
// shows the failure only as long as the encoder writes what it wrote; what
// holds the margin whatever the match finder does is
// TestEncoderMarginHoldsTheFarthestMatch.
func TestWriter2CostlyMatchAtTheChunkLimit(t *testing.T) {
	t.Parallel()

	for _, run := range []int{15600, 15601} {
		data := costlyMatchInput(run)

		var buf bytes.Buffer

		w, err := lzma.NewWriter2(&buf)
		if err != nil {
			t.Fatal(err)
		}

		if _, err = w.Write(data); err != nil {
			t.Fatalf("run %d: Write: %v", run, err)
		}

		if err = w.Close(); err != nil {
			t.Fatalf("run %d: Close: %v", run, err)
		}

		got, err := lzmaspec.DecodeLZMA2(buf.Bytes(), 8<<20)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("run %d: the reference: %d of %d bytes, %v", run, len(got), len(data), err)
		}
	}
}

// Copyright 2026 Forkcloser. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lzma //nolint:testpackage // white-box: drives the unexported encoder with operations of its choosing

import (
	"bytes"
	"errors"
	"io"
	"math/rand/v2"
	"slices"
	"testing"
)

// scriptedMatcher hands the encoder the operations it was given, in place
// of a match finder: what an operation costs the range encoder depends on
// the operations before it, not on who found them. It moves on to the next
// operation when the encoder has written one, which is when it is handed
// the bytes that operation stood for.
type scriptedMatcher struct {
	ops []operation
	// e is the encoder, for the room its range encoder has; most is the
	// most room one operation took, but for the last.
	e          *encoder
	room, most int64
}

func (m *scriptedMatcher) Write(p []byte) (int, error) {
	m.ops = m.ops[1:]

	return len(p), nil
}

func (*scriptedMatcher) SetDict(*encoderDict) {}

func (m *scriptedMatcher) NextOp([4]uint32) operation {
	room := m.e.re.Available()
	m.most = max(m.most, m.room-room)
	m.room = room

	return m.ops[0]
}

// script is a sequence of operations and the bytes they stand for.
type script struct {
	ops  []operation
	data []byte
}

func (s *script) lit(b byte) {
	s.ops = append(s.ops, litOp(b))
	s.data = append(s.data, b)
}

// match appends a match; dist0 is the distance less one, as the format
// codes it.
func (s *script) match(dist0, n int) {
	s.ops = append(s.ops, matchOp(int64(dist0)+1, n))

	for range n {
		s.data = append(s.data, s.data[len(s.data)-dist0-1])
	}
}

// far appends a match at a distance the script has no bytes behind, dist0
// the distance less one, and n bytes of filler to stand for it. The encoder
// does not look a distance up, so it codes the match as any other; what it
// writes cannot be decoded.
func (s *script) far(dist0 int64, n int) {
	s.ops = append(s.ops, matchOp(dist0+1, n))
	s.data = append(s.data, make([]byte, n)...)
}

// farthestMatch is a script that ends with the costliest match the format
// has, and the script up to that match: costlyMatch's training, with a
// distance of the last slot, 26 bits coded directly, where a distance some
// bytes stand behind in a test's dictionary has ten. The slot is 63,
// 111111: its last bit is trained with slot 62, the one before with 60,
// then 56, 48 and 32, and its first with every other match of the script,
// whose slot is 30. lits literals come before the match; how many decides
// the range it starts from, and so whether it takes nineteen bytes or
// twenty.
func farthestMatch(lits int) (s, before *script) {
	const (
		times = 200
		near  = 0x8000 // slot 30, 011110
	)

	rng := rand.New(rand.NewPCG(7, 7))
	s = &script{}

	for range 64 {
		s.lit(byte(rng.Uint32()))
	}

	train := func(base int64, n int) {
		for i := range times {
			s.far(base+16*int64(i%6), n)
		}
	}

	for _, base := range []int64{0x80000000, 0x40000000, 0x10000000, 0x01000000, 0x10000} {
		train(base, 5)
	}

	// The four low distance bits, the eight of the length's tree and its
	// two choices, as in costlyMatch.
	for _, low := range []int64{8, 4, 2} {
		train(near+low, 5)
	}

	for _, n := range []int{19, 20, 22, 26, 34, 50, 82, 146} {
		train(near+1, n)
	}

	train(near+1, 10)
	train(near+1, 5)

	for range times {
		for range 3 {
			s.lit(byte(rng.Uint32()))
		}

		s.far(near+1+16*int64((times-1)%6), 2)
	}

	for range lits {
		s.lit(byte(rng.Uint32()))
	}

	before = &script{ops: slices.Clip(s.ops), data: slices.Clip(s.data)}
	s.far(0xC0000000, 18)

	for range 8 {
		s.lit(byte(rng.Uint32()))
	}

	return s, before
}

// costlyMatch is a script that ends with a match about as costly as a
// distance within a small dictionary allows, and the script up to that match. Every modelled bit
// of it, but for a few of the distance's, meets a probability the
// operations before it drove to the other end: that it is a match after
// literals, that it is no repeat after repeats, both choices and all eight
// bits of its length, the six bits of its distance slot and the four low
// bits of its distance. A probability gives way one bit in thirty-two at a
// time, so each takes its two hundred operations, the deepest bit of a tree
// first, since training a bit walks through the ones above it.
func costlyMatch() (s, before *script) {
	const (
		times = 200
		far   = 1 << 16 // distances from here on have a slot whose first bit is 1
	)

	rng := rand.New(rand.NewPCG(20, 5))
	s = &script{}

	// Something to copy from, farther back than far.
	for range 300 {
		s.lit(byte(rng.Uint32()))
	}

	for len(s.data) < far+far/2 {
		s.match(299, maxMatchLen)
	}

	// train appends matches of length n whose distances, less one, are base
	// and the five after it sixteen apart: six, so that none is one of the
	// four last distances, which would make it a repeat.
	train := func(base, n int) {
		for i := range times {
			s.match(base+16*(i%6), n)
		}
	}

	// The distance slot of the costly match is 31, 011111: its last bit is
	// trained with slot 30, the one before with 28, and so on up; the first
	// bit, with every far match below.
	for _, base := range []int{32768, 16384, 4096, 256, 128} {
		train(base, 5)
	}

	// Its four low distance bits are 0000, the lowest coded first: trained
	// with 1000, 0100, 0010, and 0001 from here on.
	for _, low := range []int{8, 4, 2} {
		train(far+low, 5)
	}

	// Its length is 18, the first of the long ones, 00000000 in their tree.
	for _, n := range []int{19, 20, 22, 26, 34, 50, 82, 146} {
		train(far+1, n)
	}

	// The second choice of a length, then the first.
	train(far+1, 10)
	train(far+1, 5)

	// A repeat in the state three literals lead to, then literals alone.
	for range times {
		for range 3 {
			s.lit(byte(rng.Uint32()))
		}

		s.match(far+1+16*((times-1)%6), 2)
	}

	for range 4 * times {
		s.lit(byte(rng.Uint32()))
	}

	before = &script{ops: slices.Clip(s.ops), data: slices.Clip(s.data)}
	s.match(49152, 18)

	for range 8 {
		s.lit(byte(rng.Uint32()))
	}

	return s, before
}

// encodeScript runs the script through an encoder whose output is limited
// to limit bytes, as an LZMA2 chunk's is, and closes it where the limit
// stops it, as Writer2 does, with the end-of-stream marker if flags ask for
// it.
func encodeScript(t *testing.T, s *script, limit int64, flags encoderFlags) encoded {
	t.Helper()

	const dictCap = 1 << 17

	m := &scriptedMatcher{ops: s.ops}

	dict, err := newEncoderDict(dictCap, lookaheadSize, m)
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer

	e := newEncoder(&limitedByteWriter{BW: &buf, N: limit}, newState(Properties{LC: 3, LP: 0, PB: 2}), dict, flags)
	m.e = e

	if _, err = e.Write(s.data); err != nil && !errors.Is(err, errLimit) {
		t.Fatal(err)
	}

	err = e.Close()

	return encoded{out: buf.Bytes(), n: e.Compressed(), most: m.most, err: err}
}

// encoded is what encodeScript made of a script.
type encoded struct {
	// out is what the encoder wrote, and n how many bytes of the script
	// that stands for.
	out []byte
	n   int64
	// most is the most room one operation took of the range encoder.
	most int64
	// err is Close's error.
	err error
}

// TestEncoderMarginHoldsACostlyMatch: "errLimit ... is internal to the
// encoder: Writer2 turns it into a chunk boundary", which holds only if the
// encoder stops before an operation it has no room to finish and to close
// the range coder after. A match of eighteen bytes, whose bytes are there to
// be decoded, is put where the limit falls, at every distance from it,
// without the end-of-stream marker and with it: wherever the limit falls,
// Close succeeds, and what was written decodes to the bytes the encoder
// says it compressed.
func TestEncoderMarginHoldsACostlyMatch(t *testing.T) {
	t.Parallel()

	s, before := costlyMatch()

	// Without a limit, for what the operations before the match come to.
	whole := encodeScript(t, s, maxInt64, 0)
	if whole.err != nil || whole.n != int64(len(s.data)) {
		t.Fatalf("no limit: %d of %d bytes, %v", whole.n, len(s.data), whole.err)
	}

	t.Logf("%d operations in %d bytes; one took %d bytes of room", len(s.ops), len(whole.out), whole.most)

	if whole.most <= 16 || whole.most > maxOpLen {
		t.Fatalf("the script's match took %d bytes of room: more than 16, the margin that was, "+
			"and at most %d are expected", whole.most, maxOpLen)
	}

	head := encodeScript(t, before, maxInt64, 0)
	if head.err != nil {
		t.Fatal(head.err)
	}

	start := int64(len(head.out))

	for _, flags := range []encoderFlags{0, eosMarker} {
		for limit := start - 8; limit < start+64; limit++ {
			got := encodeScript(t, s, limit, flags)
			if got.err != nil {
				t.Errorf("marker %v, limit %d, %d bytes past the operations before the match: Close: %v",
					flags != 0, limit, limit-start, got.err)

				continue
			}

			if int64(len(got.out)) > limit {
				t.Errorf("marker %v, limit %d: %d bytes written", flags != 0, limit, len(got.out))
			}

			dict, err := newDecoderDict(1 << 17)
			if err != nil {
				t.Fatal(err)
			}

			d, err := newDecoder(bytes.NewBuffer(got.out), newState(Properties{LC: 3, LP: 0, PB: 2}), dict, got.n)
			if err != nil {
				t.Fatalf("marker %v, limit %d: %v", flags != 0, limit, err)
			}

			if data, rerr := io.ReadAll(d); rerr != nil || !bytes.Equal(data, s.data[:got.n]) {
				t.Errorf("marker %v, limit %d: %d bytes decoded of %d, %v", flags != 0, limit, len(data), got.n, rerr)
			}
		}
	}
}

// TestEncoderMarginHoldsTheFarthestMatch: maxOpLen is "twenty bytes and no
// more", and the margin holds an operation of that many and the five Close
// needs. The farthest match, every bit of it trained against, takes twenty
// bytes of room from some ranges and nineteen from the others, never more;
// one that takes twenty is put at every distance from a limit, without the
// end-of-stream marker and with it: Close succeeds, and nothing is written
// past the limit. What is written is not decoded: no bytes stand behind
// the distance.
func TestEncoderMarginHoldsTheFarthestMatch(t *testing.T) {
	t.Parallel()

	var s, before *script

	for lits := 800; lits < 1056; lits++ {
		full, head := farthestMatch(lits)

		got := encodeScript(t, full, maxInt64, 0)
		if got.err != nil || got.n != int64(len(full.data)) {
			t.Fatalf("%d literals, no limit: %d of %d bytes, %v", lits, got.n, len(full.data), got.err)
		}

		if got.most != maxOpLen && got.most != maxOpLen-1 {
			t.Fatalf("%d literals: the farthest match took %d bytes of room; %d or %d are expected",
				lits, got.most, maxOpLen-1, maxOpLen)
		}

		if got.most == maxOpLen && s == nil {
			s, before = full, head
		}
	}

	if s == nil || before == nil {
		t.Fatalf("no farthest match took %d bytes of room", maxOpLen)
	}

	head := encodeScript(t, before, maxInt64, 0)
	if head.err != nil {
		t.Fatal(head.err)
	}

	start := int64(len(head.out))

	for _, flags := range []encoderFlags{0, eosMarker} {
		for limit := start - 8; limit < start+64; limit++ {
			got := encodeScript(t, s, limit, flags)
			if got.err != nil {
				t.Errorf("marker %v, limit %d, %d bytes past the operations before the match: Close: %v",
					flags != 0, limit, limit-start, got.err)
			}

			if int64(len(got.out)) > limit {
				t.Errorf("marker %v, limit %d: %d bytes written", flags != 0, limit, len(got.out))
			}
		}
	}
}

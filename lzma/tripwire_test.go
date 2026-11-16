// Copyright 2026 Forkcloser. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lzma_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"math/rand/v2"
	"testing"

	"github.com/forkcloser/xz/v2/internal/lzmaspec"
	"github.com/forkcloser/xz/v2/lzma"
)

// seeded is the tripwire's source of numbers: a PCG stream, which the
// language fixes, reduced by the test itself, so the inputs are the same
// bytes under every release of Go.
type seeded struct {
	src *rand.PCG
}

// below returns a number below k.
func (s seeded) below(k int) int {
	return int((s.src.Uint64() >> 32) % uint64(k))
}

// prose is n bytes of words drawn from a vocabulary of three hundred, the
// common ones more often: matches of every length at every distance.
func prose(n int) []byte {
	s := seeded{src: rand.NewPCG(1, 1)}

	vocabulary := make([][]byte, 300)
	for i := range vocabulary {
		word := make([]byte, 2+s.below(9))
		for j := range word {
			word[j] = byte('a' + s.below(26))
		}

		vocabulary[i] = word
	}

	out := make([]byte, 0, n+16)
	for len(out) < n {
		out = append(out, vocabulary[min(s.below(len(vocabulary)), s.below(len(vocabulary)))]...)

		switch s.below(12) {
		case 0:
			out = append(out, '.', '\n')
		case 1:
			out = append(out, ',', ' ')
		default:
			out = append(out, ' ')
		}
	}

	return out[:n]
}

// records is n bytes of sixteen-byte records, a counter and fields of a
// few values each: the same few distances over and over, which is what the
// four repeat distances are for.
func records(n int) []byte {
	s := seeded{src: rand.NewPCG(2, 2)}

	out := make([]byte, 0, n+16)
	for i := 0; len(out) < n; i++ {
		out = append(out,
			byte(i), byte(i>>8), 0, 0,
			byte(s.below(3)), 0xff, byte(s.below(2))*0x80, 7,
			'k', 'e', 'y', byte('0'+s.below(10)),
			byte(s.below(256)), 0, 0, byte(s.below(5)),
		)
	}

	return out[:n]
}

// noise is n bytes of seven random bits each: literals that compress by an
// eighth, so an LZMA2 chunk ends at its compressed limit and is still
// worth writing compressed.
func noise(n int) []byte {
	s := seeded{src: rand.NewPCG(3, 3)}

	out := make([]byte, n)
	for i := range out {
		out[i] = byte(s.below(128))
	}

	return out
}

// TestTripwireEncodedBytes pins the bytes the two writers produce for three
// fixed inputs, under the default dictionary and the smallest.
//
// It is not a promise to callers: nothing says a release writes the bytes
// the one before it wrote, and a better match finder is free to write
// others. It is a tripwire. Every other test of the writers asks only that
// a stream decodes to its input, which an encoder that finds no match at
// all, or loses the far ones, or cuts its chunks elsewhere, still passes:
// the stream is valid, only larger. A change in what the encoder writes
// fails here, so that it is seen and decided, not shipped unnoticed. When
// the change is meant, the failure prints the row to put in its place;
// compare the lengths first, they are the compression lost or gained.
//
// The reference decoder stays the judge of validity: each stream is held
// to it before its bytes are compared.
func TestTripwireEncodedBytes(t *testing.T) {
	t.Parallel()

	inputs := map[string][]byte{
		"prose":   prose(96 << 10),
		"records": records(64 << 10),
		"noise":   noise(96 << 10),
	}

	for _, pin := range []struct {
		input, format string
		// dict is the dictionary in bytes, the default when zero.
		dict int
		size int
		sum  string
	}{
		{"prose", "classic", 0, 28715, "00d7be0427fd35ecae0179880f796c4d924c3a9d2eee8f6caad43470a8162a1d"},
		{"prose", "classic", 4096, 31720, "a597aec539dec80832985cfd5fe8de83973fe7f3472fd0797752bcb0b67d55ad"},
		{"prose", "lzma2", 0, 28703, "52ecf30d94d9881eef018b2e7c0aaea6fd96ad099e880f510228da47273c8e9b"},
		{"prose", "lzma2", 4096, 31707, "1327ee9fe796a1e9719b4e50122aeed43339816913e1d2632ef58e510ee0da98"},
		{"records", "classic", 0, 17586, "68e2ca4fea002e77a72ffd9f97954c752dfbea4178752a5b057fdfe9ab7f2445"},
		{"records", "classic", 4096, 18005, "8d7f45c235a62ad68c63fffb92fd35dc9152664556c45915c00f72a584f47782"},
		{"records", "lzma2", 0, 17574, "976d7ae306473027b403ed5f774cece5df9e45fb804a7e040a64009aa80e08b0"},
		{"records", "lzma2", 4096, 17993, "123e93bbded6a3edd0ecfaeae4fef3d97570094b1c9f634ceedec5d1ddb661dd"},
		{"noise", "classic", 0, 87687, "68209dd3485a98509625abb448f4ee208f187de4a6cc2518cab78f51f72cdd1f"},
		{"noise", "classic", 4096, 87688, "5c899ca6ed7bac1f663fd3f93bd33ee41f53275dbf3eb59a1ffa2722cef12f48"},
		{"noise", "lzma2", 0, 87684, "30082ca90b4e94264acbcbfe3e85683b998ccd6ce37315fcbc733be499e5dab4"},
		{"noise", "lzma2", 4096, 87685, "da71e220df65d34b46c1439f3479c1720448403c608d1e12079aae0c6d86464e"},
	} {
		data, ok := inputs[pin.input]
		if !ok {
			t.Fatalf("no such input: %s", pin.input)
		}

		var (
			opts   []lzma.Option
			stream []byte
		)

		dict := defaultDict
		if pin.dict != 0 {
			dict = pin.dict
			opts = append(opts, lzma.WithDictSize(dict))
		}

		switch pin.format {
		case "classic":
			stream = classic(t, wcase{opts: opts}, data)

			out, err := lzmaspec.Decode(stream)
			if err != nil || !bytes.Equal(out.Data, data) {
				t.Fatalf("%s, %s, %d: the reference: %d bytes, %v", pin.input, pin.format, pin.dict, len(out.Data), err)
			}
		case "lzma2":
			stream = lzma2(t, w2case{opts: opts}, data)

			got, err := lzmaspec.DecodeLZMA2(stream, uint32(dict))
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("%s, %s, %d: the reference: %d bytes, %v", pin.input, pin.format, pin.dict, len(got), err)
			}
		default:
			t.Fatalf("no such format: %s", pin.format)
		}

		sum := sha256.Sum256(stream)
		if got := hex.EncodeToString(sum[:]); len(stream) != pin.size || got != pin.sum {
			t.Errorf("the encoder writes other bytes, %d of them where it wrote %d; if meant, the row is now\n"+
				"{%q, %q, %d, %d, %q},", len(stream), pin.size, pin.input, pin.format, pin.dict, len(stream), got)
		}
	}
}

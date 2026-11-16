// Copyright 2026 Forkcloser. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package filters_test

import (
	"bytes"
	"encoding/hex"
	"errors"
	"math/rand/v2"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/forkcloser/xz/v2/internal/filters"
)

// bcjIDs lists every BCJ filter with its alignment.
func bcjIDs() []struct {
	name  string
	id    uint64
	align uint32
} {
	return []struct {
		name  string
		id    uint64
		align uint32
	}{
		{"x86", filters.X86ID, 1},
		{"powerpc", filters.PowerPCID, 4},
		{"ia64", filters.IA64ID, 16},
		{"arm", filters.ARMID, 4},
		{"armthumb", filters.ARMThumbID, 2},
		{"sparc", filters.SPARCID, 4},
		{"arm64", filters.ARM64ID, 4},
		{"riscv", filters.RISCVID, 2},
	}
}

// corpus returns inputs that exercise every converter: uniform random
// bytes, and bytes drawn from the opcodes the filters look for, so that
// the instruction patterns are dense and their neighbours vary.
func corpus(seed uint64, n int) [][]byte {
	rng := rand.New(rand.NewPCG(seed, 1))

	random := make([]byte, n)
	for i := range random {
		random[i] = byte(rng.Uint32())
	}

	opcodes := []byte{
		0x00, 0xFF, 0xE8, 0xE9, 0xEB, 0xF0, 0xF8, 0x48, 0x4B, 0x40, 0x7F,
		0xC0, 0x94, 0x97, 0x90, 0xB0, 0x17, 0xEF, 0x03, 0x67, 0x05, 0x10, 0x16, 0x18,
	}

	dense := make([]byte, n)
	for i := range dense {
		dense[i] = opcodes[rng.IntN(len(opcodes))]
	}

	return [][]byte{random, dense, craftedBranches(rng)}
}

// craftedBranches returns instructions the sparser patterns of random bytes
// rarely form: IA-64 bundles whose template admits a branch and whose slots
// hold a br.call, and PowerPC bl instructions, each with random operands.
func craftedBranches(rng *rand.Rand) []byte {
	templates := []byte{16, 17, 18, 19, 22, 23, 24, 25, 28, 29}

	var out []byte

	for range 128 {
		// A bundle is 5 template bits then three 41-bit slots, least
		// significant bit first.
		var bits [3]uint64
		for s := range bits {
			v := rng.Uint64() & (1<<41 - 1)
			v &^= 0xF << 37
			v |= 0x5 << 37
			v &^= 0x7 << 9
			bits[s] = v
		}

		var bundle [16]byte

		bundle[0] = templates[rng.IntN(len(templates))] | byte(rng.Uint32()&0xE0)

		pos := uint(5)

		for _, v := range bits {
			for b := range uint(41) {
				if v>>b&1 != 0 {
					bundle[(pos+b)>>3] |= 1 << ((pos + b) & 7)
				}
			}

			pos += 41
		}

		out = append(out, bundle[:]...)
	}

	for range 256 {
		r := rng.Uint32()
		out = append(out, 0x48|byte(r&3), byte(r>>8), byte(r>>16), byte(r>>24)&^3|1)
	}

	return out
}

// codeAll runs a converter over data in one call per chunk, carrying the
// unconverted tail into the next chunk as a stream would, and converts the
// final tail by leaving it alone, as the format does at the end of the data.
func codeAll(t *testing.T, b *filters.BCJ, data []byte, chunk func() int) []byte {
	t.Helper()

	out := make([]byte, 0, len(data))
	buf := make([]byte, 0, len(data))

	for len(data) > 0 {
		k := min(chunk(), len(data))
		buf = append(buf, data[:k]...)
		data = data[k:]

		n := b.Code(buf)
		if n > len(buf) {
			t.Fatalf("Code converted %d of %d bytes", n, len(buf))
		}

		if n == 0 && len(buf) >= b.Lookahead() && len(data) > 0 {
			t.Fatalf("no progress on %d bytes, lookahead %d", len(buf), b.Lookahead())
		}

		out = append(out, buf[:n]...)
		buf = append(buf[:0], buf[n:]...)
	}

	return append(out, buf...)
}

func TestBCJRoundTrip(t *testing.T) {
	t.Parallel()

	for _, f := range bcjIDs() {
		for _, start := range []uint32{0, f.align * 3, 0x1000, 0xFFFFF000} {
			t.Run(f.name, func(t *testing.T) {
				t.Parallel()

				for _, data := range corpus(f.id, 1<<16) {
					enc, err := filters.NewBCJ(f.id, start, true)
					if err != nil {
						t.Fatal(err)
					}

					dec, err := filters.NewBCJ(f.id, start, false)
					if err != nil {
						t.Fatal(err)
					}

					rng := rand.New(rand.NewPCG(uint64(start), 2))
					chunk := func() int { return 1 + rng.IntN(300) }

					encoded := codeAll(t, enc, bytes.Clone(data), chunk)
					if len(encoded) != len(data) {
						t.Fatalf("encoded %d bytes, want %d", len(encoded), len(data))
					}

					decoded := codeAll(t, dec, bytes.Clone(encoded), chunk)
					if !bytes.Equal(decoded, data) {
						t.Fatalf("start 0x%x: decode(encode(data)) differs from data", start)
					}
				}
			})
		}
	}
}

// TestBCJChunking holds a converter to the same output whatever the chunk
// sizes it sees the data in: the reader's contract.
func TestBCJChunking(t *testing.T) {
	t.Parallel()

	for _, f := range bcjIDs() {
		t.Run(f.name, func(t *testing.T) {
			t.Parallel()

			for _, data := range corpus(f.id+100, 1<<15) {
				whole, err := filters.NewBCJ(f.id, 0, true)
				if err != nil {
					t.Fatal(err)
				}

				want := codeAll(t, whole, bytes.Clone(data), func() int { return len(data) })

				for _, size := range []int{1, 2, 3, 5, 7, 16, 17, 64, 1000} {
					b, err := filters.NewBCJ(f.id, 0, true)
					if err != nil {
						t.Fatal(err)
					}

					got := codeAll(t, b, bytes.Clone(data), func() int { return size })
					if !bytes.Equal(got, want) {
						t.Fatalf("chunks of %d bytes convert differently from one call", size)
					}
				}
			}
		})
	}
}

func TestBCJConverts(t *testing.T) {
	t.Parallel()

	// A CALL at offset 0 with displacement 0x10: the encoder makes the
	// target absolute, 0x10 + 5.
	b, err := filters.NewBCJ(filters.X86ID, 0, true)
	if err != nil {
		t.Fatal(err)
	}

	// Of the ten bytes, the call and the NOP after it are decided; the
	// last four could begin a call whose displacement is not here yet.
	buf := []byte{0xE8, 0x10, 0x00, 0x00, 0x00, 0x90, 0x90, 0x90, 0x90, 0x90}
	if n := b.Code(buf); n != 6 {
		t.Fatalf("converted %d bytes, want 6", n)
	}

	if want := []byte{0xE8, 0x15, 0x00, 0x00, 0x00}; !bytes.Equal(buf[:5], want) {
		t.Fatalf("got % x, want % x", buf[:5], want)
	}
}

func TestBCJErrors(t *testing.T) {
	t.Parallel()

	if _, err := filters.NewBCJ(0x21, 0, false); err == nil {
		t.Fatal("LZMA2's id accepted as a BCJ filter")
	}

	for _, f := range bcjIDs() {
		if f.align == 1 {
			continue
		}

		if _, err := filters.NewBCJ(f.id, f.align/2, false); !errors.Is(err, filters.ErrAlignment) {
			t.Fatalf("%s: start offset %d: %v, want ErrAlignment", f.name, f.align/2, err)
		}
	}

	if !filters.IsBCJ(filters.X86ID) || filters.IsBCJ(filters.DeltaID) {
		t.Fatal("IsBCJ wrong")
	}
}

func TestDelta(t *testing.T) {
	t.Parallel()

	d, err := filters.NewDelta(1)
	if err != nil {
		t.Fatal(err)
	}

	buf := []byte{1, 2, 3, 5, 8}
	d.Encode(buf)

	if want := []byte{1, 1, 1, 2, 3}; !bytes.Equal(buf, want) {
		t.Fatalf("encode: got %v, want %v", buf, want)
	}

	for _, dist := range []int{1, 2, 3, 4, 255, 256} {
		for _, data := range corpus(uint64(dist), 1<<14) {
			enc, err := filters.NewDelta(dist)
			if err != nil {
				t.Fatal(err)
			}

			dec, err := filters.NewDelta(dist)
			if err != nil {
				t.Fatal(err)
			}

			buf := bytes.Clone(data)
			// In pieces: the coder's history spans calls.
			for i := 0; i < len(buf); i += 777 {
				enc.Encode(buf[i:min(i+777, len(buf))])
			}

			for i := 0; i < len(buf); i += 333 {
				dec.Decode(buf[i:min(i+333, len(buf))])
			}

			if !bytes.Equal(buf, data) {
				t.Fatalf("distance %d: decode(encode(data)) differs from data", dist)
			}
		}
	}

	for _, dist := range []int{0, 257} {
		if _, err := filters.NewDelta(dist); !errors.Is(err, filters.ErrDistance) {
			t.Fatalf("distance %d: %v, want ErrDistance", dist, err)
		}
	}
}

// bcjVector is a hand-built instruction sequence at a boundary of what a
// converter converts, and what XZ Utils makes of it at start offset zero:
// its encoder's output, and its decoder's, which is what reading a filtered
// block that holds the sequence gives. TestBCJVectorsFromXZUtils derives
// both again from the binary on PATH.
type bcjVector struct {
	name string
	id   uint64
	// option names the filter to xz-utils.
	option string

	plain, encoded, decoded []byte
}

// unhex is the bytes a hexadecimal string spells, its spaces and line
// breaks aside.
func unhex(t *testing.T, s string) []byte {
	t.Helper()

	b, err := hex.DecodeString(strings.Join(strings.Fields(s), ""))
	if err != nil {
		t.Fatal(err)
	}

	return b
}

// ia64Templates is an empty IA-64 bundle, then a bundle of each of the 32
// templates in turn, the three slots of each a br.call to the bundle
// itself: opcode 5 in bits 37 to 40 of the slot and nothing else.
func ia64Templates() []byte {
	out := make([]byte, 16, 33*16)

	for template := range byte(32) {
		out = append(out, template, 0, 0, 0, 0, 0x14, 0, 0, 0, 0, 0x28, 0, 0, 0, 0, 0x50)
	}

	return out
}

// bcjVectors are the vectors.
func bcjVectors(t *testing.T) []bcjVector {
	t.Helper()

	vector := func(name string, id uint64, option, plain, encoded, decoded string) bcjVector {
		return bcjVector{name, id, option, unhex(t, plain), unhex(t, encoded), unhex(t, decoded)}
	}

	return []bcjVector{
		// The SPARC filter converts a call whose word offset fits 22 bits
		// and a sign, and what it stores wraps within them. A nop, then a
		// call at address 4, where the encoder adds a word to the offset
		// and the decoder takes one away: the furthest offset forwards,
		// which the encoder wraps, and the one before it; the furthest
		// backwards, which the decoder wraps; and one word backwards,
		// which the encoder takes to zero.
		vector("sparc/offset +0x3ffffe words", filters.SPARCID, "--sparc",
			"01000000 403ffffe", "01000000 403fffff", "01000000 403ffffd"),
		vector("sparc/offset +0x3fffff words", filters.SPARCID, "--sparc",
			"01000000 403fffff", "01000000 7fc00000", "01000000 403ffffe"),
		vector("sparc/offset -0x400000 words", filters.SPARCID, "--sparc",
			"01000000 7fc00000", "01000000 7fc00001", "01000000 403fffff"),
		vector("sparc/offset -1 word", filters.SPARCID, "--sparc",
			"01000000 7fffffff", "01000000 40000000", "01000000 7ffffffe"),
		// A RISC-V AUIPC with rd x2 and 3 in bits 12 and 13 is the
		// encoder's marker of a converted pair, its top five bits the
		// pair's register, unless they name x0 or x2: the encoder leaves
		// a pair with either alone, so that AUIPC is a program's own and
		// stays as it is. A nop, the AUIPC at address 4, two nops.
		vector("riscv/auipc x2, register x0", filters.RISCVID, "--riscv",
			"13000000 17310000 13000000 13000000",
			"13000000 17310000 13000000 13000000",
			"13000000 17310000 13000000 13000000"),
		vector("riscv/auipc x2, register x2", filters.RISCVID, "--riscv",
			"13000000 17310010 13000000 13000000",
			"13000000 17310010 13000000 13000000",
			"13000000 17310010 13000000 13000000"),
		vector("riscv/auipc x2, register x1", filters.RISCVID, "--riscv",
			"13000000 17310008 13000000 13000000",
			"13000000 97000000 03803001 13000000",
			"13000000 97000013 0380c0ff 13000000"),
		// An IA-64 template decides which slots of its bundle may hold a
		// branch: 16 and 17 the last, 18 and 19 the last two, 22 and 23
		// all three, 24, 25, 28 and 29 the last, every other none.
		{
			"ia64/every template", filters.IA64ID, "--ia64",
			ia64Templates(),
			unhex(t, `
				00000000000000000000000000000000 00000000001400000000280000000050
				01000000001400000000280000000050 02000000001400000000280000000050
				03000000001400000000280000000050 04000000001400000000280000000050
				05000000001400000000280000000050 06000000001400000000280000000050
				07000000001400000000280000000050 08000000001400000000280000000050
				09000000001400000000280000000050 0a000000001400000000280000000050
				0b000000001400000000280000000050 0c000000001400000000280000000050
				0d000000001400000000280000000050 0e000000001400000000280000000050
				0f000000001400000000280000000050 10000000001400000000280010010050
				11000000001400000000280020010050 12000000001400980000280030010050
				13000000001400a00000280040010050 14000000001400000000280000000050
				15000000001400000000280000000050 16005c00001400b80000280070010050
				17006000001400c00000280080010050 18000000001400000000280090010050
				190000000014000000002800a0010050 1a000000001400000000280000000050
				1b000000001400000000280000000050 1c0000000014000000002800d0010050
				1d0000000014000000002800e0010050 1e000000001400000000280000000050
				1f000000001400000000280000000050`),
			unhex(t, `
				00000000000000000000000000000000 00000000001400000000280000000050
				01000000001400000000280000000050 02000000001400000000280000000050
				03000000001400000000280000000050 04000000001400000000280000000050
				05000000001400000000280000000050 06000000001400000000280000000050
				07000000001400000000280000000050 08000000001400000000280000000050
				09000000001400000000280000000050 0a000000001400000000280000000050
				0b000000001400000000280000000050 0c000000001400000000280000000050
				0d000000001400000000280000000050 0e000000001400000000280000000050
				0f000000001400000000280000000050 100000000014000000002800f0feff58
				110000000014000000002800e0feff58 1200000000140068ff7f2c00d0feff58
				1300000000140060ff7f2c00c0feff58 14000000001400000000280000000050
				15000000001400000000280000000050 1600a4ff3f160048ff7f2c0090feff58
				1700a0ff3f160040ff7f2c0080feff58 18000000001400000000280070feff58
				19000000001400000000280060feff58 1a000000001400000000280000000050
				1b000000001400000000280000000050 1c000000001400000000280030feff58
				1d000000001400000000280020feff58 1e000000001400000000280000000050
				1f000000001400000000280000000050`),
		},
	}
}

// codeVector converts data from stream position zero, in one call and then
// a byte at a time, and fails the test when the two differ.
func codeVector(t *testing.T, id uint64, encode bool, data []byte) []byte {
	t.Helper()

	var out [2][]byte

	for i, size := range []int{len(data), 1} {
		b, err := filters.NewBCJ(id, 0, encode)
		if err != nil {
			t.Fatal(err)
		}

		out[i] = codeAll(t, b, bytes.Clone(data), func() int { return size })
	}

	if !bytes.Equal(out[0], out[1]) {
		t.Fatalf("a byte at a time: % x, in one call: % x", out[1], out[0])
	}

	return out[0]
}

// TestBCJVectors holds the converters to XZ Utils at the boundaries of what
// they convert: of each vector's sequence, Code in the encoding direction
// makes XZ Utils' encoding and in the decoding direction XZ Utils'
// decoding, and it decodes the encoding to the sequence.
func TestBCJVectors(t *testing.T) {
	t.Parallel()

	for _, v := range bcjVectors(t) {
		t.Run(v.name, func(t *testing.T) {
			t.Parallel()

			for _, c := range []struct {
				what     string
				encode   bool
				in, want []byte
			}{
				{"encoding the sequence", true, v.plain, v.encoded},
				{"decoding the sequence", false, v.plain, v.decoded},
				{"decoding the encoding", false, v.encoded, v.plain},
			} {
				got := codeVector(t, v.id, c.encode, c.in)
				if !bytes.Equal(got, c.want) {
					at := 0
					for at < len(got) && got[at] == c.want[at] {
						at++
					}

					// The sixteen bytes the first difference is in.
					lo := at &^ 15
					hi := min(lo+16, len(got))

					t.Errorf("%s differs from XZ Utils at byte %d: of % x, got % x, want % x",
						c.what, at, c.in[lo:hi], got[lo:hi], c.want[lo:hi])
				}
			}
		})
	}
}

// xzUtils is the path of the XZ Utils binary, xz-utils on PATH. Without
// one the test is skipped, or fails when XZ_UTILS_REQUIRED is 1, as the
// root package's interop lane does.
func xzUtils(t *testing.T) string {
	t.Helper()

	path, err := exec.LookPath("xz-utils")
	if err != nil {
		if os.Getenv("XZ_UTILS_REQUIRED") == "1" {
			t.Fatalf("xz-utils is not on PATH: %v", err)
		}

		t.Skipf("xz-utils is not on PATH: %v", err)
	}

	out, err := exec.CommandContext(t.Context(), path, "--version").Output()
	if err != nil {
		t.Fatalf("%s --version: %v", path, err)
	}

	if !strings.HasPrefix(string(out), "xz (XZ Utils) ") {
		t.Fatalf("%s is not XZ Utils: %q", path, bytes.SplitN(out, []byte("\n"), 2)[0])
	}

	return path
}

// runXZ runs xz-utils on stdin and returns its stdout, the environment's
// own options cleared.
func runXZ(t *testing.T, bin string, stdin []byte, args ...string) []byte {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), bin, args...)

	cmd.Env = append(os.Environ(), "XZ_DEFAULTS=", "XZ_OPT=")
	cmd.Stdin = bytes.NewReader(stdin)

	var stderr bytes.Buffer

	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("xz %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}

	return out
}

// TestBCJVectorsFromXZUtils holds the vectors to the xz-utils binary on
// PATH. A raw stream written through the filter and LZMA2, read back
// through LZMA2 alone, is the filter's encoding; one written through LZMA2
// alone and read back through the filter and LZMA2 is its decoding.
func TestBCJVectorsFromXZUtils(t *testing.T) {
	t.Parallel()

	bin := xzUtils(t)

	const lzma2 = "--lzma2=preset=0"

	for _, v := range bcjVectors(t) {
		t.Run(v.name, func(t *testing.T) {
			t.Parallel()

			filtered := runXZ(t, bin, v.plain, "-c", "--format=raw", v.option, lzma2)
			if got := runXZ(t, bin, filtered, "-dc", "--format=raw", lzma2); !bytes.Equal(got, v.encoded) {
				t.Errorf("xz encodes % x to % x, the vector says % x", v.plain, got, v.encoded)
			}

			unfiltered := runXZ(t, bin, v.plain, "-c", "--format=raw", lzma2)
			if got := runXZ(t, bin, unfiltered, "-dc", "--format=raw", v.option, lzma2); !bytes.Equal(got, v.decoded) {
				t.Errorf("xz decodes % x to % x, the vector says % x", v.plain, got, v.decoded)
			}
		})
	}
}

// TestBCJLookahead: Lookahead is the shortest buffer Code is sure to make
// progress on. Of that many bytes it converts at least one, and of one
// fewer it converts none, whatever the bytes are.
func TestBCJLookahead(t *testing.T) {
	t.Parallel()

	for _, f := range bcjIDs() {
		t.Run(f.name, func(t *testing.T) {
			t.Parallel()

			// code is how many of buf's bytes a new converter converts.
			code := func(buf []byte) int {
				b, err := filters.NewBCJ(f.id, 0, true)
				if err != nil {
					t.Fatal(err)
				}

				return b.Code(bytes.Clone(buf))
			}

			b, err := filters.NewBCJ(f.id, 0, true)
			if err != nil {
				t.Fatal(err)
			}

			size := b.Lookahead()

			for _, data := range corpus(f.id+200, 1<<10) {
				for at := 0; at+size <= len(data); at++ {
					window := data[at : at+size]

					if n := code(window[:size-1]); n != 0 {
						t.Fatalf("converted %d of the %d bytes % x, one fewer than the lookahead",
							n, size-1, window[:size-1])
					}

					if n := code(window); n < 1 {
						t.Fatalf("converted none of the %d bytes % x, the lookahead", size, window)
					}
				}
			}
		})
	}
}

// Copyright 2026 Forkcloser. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package xz_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/forkcloser/xz/v2"
	"github.com/forkcloser/xz/v2/lzma"
)

// The BCJ and delta filters, held to XZ Utils: testdata/filters holds, for
// each filter, start offset and distance below, what `xz` makes of one
// fixed input, and both readers must decode each file back to that input.
// The input is regenerated here from its seed, so only the compressed
// files are committed; XZ_UPDATE_FILTER_GOLDEN=1 rewrites them with the
// xz-utils binary on PATH.

// filterCase is one golden file: its name, the xz options that made it,
// and the same chain and block size as the writer takes them.
type filterCase struct {
	name      string
	args      []string
	chain     []xz.Filter
	blockSize int64
}

// filterCases are the golden files.
func filterCases() []filterCase {
	return []filterCase{
		{"x86", []string{"--x86"}, []xz.Filter{{ID: xz.X86}}, 0},
		{"x86-start", []string{"--x86=start=4096"}, []xz.Filter{{ID: xz.X86, StartOffset: 4096}}, 0},
		{"powerpc", []string{"--powerpc"}, []xz.Filter{{ID: xz.PowerPC}}, 0},
		{"powerpc-start", []string{"--powerpc=start=4096"}, []xz.Filter{{ID: xz.PowerPC, StartOffset: 4096}}, 0},
		{"ia64", []string{"--ia64"}, []xz.Filter{{ID: xz.IA64}}, 0},
		{"ia64-start", []string{"--ia64=start=4096"}, []xz.Filter{{ID: xz.IA64, StartOffset: 4096}}, 0},
		{"arm", []string{"--arm"}, []xz.Filter{{ID: xz.ARM}}, 0},
		{"arm-start", []string{"--arm=start=4096"}, []xz.Filter{{ID: xz.ARM, StartOffset: 4096}}, 0},
		{"armthumb", []string{"--armthumb"}, []xz.Filter{{ID: xz.ARMThumb}}, 0},
		{"armthumb-start", []string{"--armthumb=start=4098"}, []xz.Filter{{ID: xz.ARMThumb, StartOffset: 4098}}, 0},
		{"sparc", []string{"--sparc"}, []xz.Filter{{ID: xz.SPARC}}, 0},
		{"sparc-start", []string{"--sparc=start=4096"}, []xz.Filter{{ID: xz.SPARC, StartOffset: 4096}}, 0},
		{"arm64", []string{"--arm64"}, []xz.Filter{{ID: xz.ARM64}}, 0},
		{"arm64-start", []string{"--arm64=start=8192"}, []xz.Filter{{ID: xz.ARM64, StartOffset: 8192}}, 0},
		{"riscv", []string{"--riscv"}, []xz.Filter{{ID: xz.RISCV}}, 0},
		{"riscv-start", []string{"--riscv=start=4098"}, []xz.Filter{{ID: xz.RISCV, StartOffset: 4098}}, 0},
		{"delta-1", []string{"--delta=dist=1"}, []xz.Filter{{ID: xz.Delta, Distance: 1}}, 0},
		{"delta-4", []string{"--delta=dist=4"}, []xz.Filter{{ID: xz.Delta, Distance: 4}}, 0},
		{"delta-256", []string{"--delta=dist=256"}, []xz.Filter{{ID: xz.Delta, Distance: 256}}, 0},
		{"delta-x86", []string{"--delta=dist=2", "--x86"}, []xz.Filter{{ID: xz.Delta, Distance: 2}, {ID: xz.X86}}, 0},
		{
			"x86-delta-arm",
			[]string{"--x86", "--delta=dist=3", "--arm"},
			[]xz.Filter{{ID: xz.X86}, {ID: xz.Delta, Distance: 3}, {ID: xz.ARM}},
			0,
		},
		{"blocks-arm64", []string{"--arm64", "--block-size=4096"}, []xz.Filter{{ID: xz.ARM64}}, 4096},
	}
}

// filterInput is the input every golden file compresses: bytes drawn from
// the opcodes the filters look for, so their instruction patterns are dense
// and their operands vary, then crafted IA-64 and PowerPC branches, whose
// patterns random bytes rarely form, then a run of uniform random bytes.
func filterInput() []byte {
	rng := rand.New(rand.NewPCG(0x6263, 0x6A))

	opcodes := []byte{
		0x00, 0xFF, 0xE8, 0xE9, 0xEB, 0xF0, 0xF8, 0x48, 0x40, 0x7F,
		0xC0, 0x94, 0x90, 0x17, 0xEF, 0x05,
	}

	data := make([]byte, 16<<10, 24<<10)
	for i := range data {
		data[i] = opcodes[rng.IntN(len(opcodes))]
	}

	data = append(data, craftedBranches(rng)...)

	for range 4 << 10 {
		data = append(data, byte(rng.Uint32()))
	}

	return data
}

// craftedBranches returns instructions the sparser patterns of random bytes
// rarely form: IA-64 bundles whose template admits a branch and whose slots
// hold a br.call, and PowerPC bl instructions, each with random operands.
func craftedBranches(rng *rand.Rand) []byte {
	templates := []byte{16, 17, 18, 19, 22, 23, 24, 25, 28, 29}

	// 128 bundles of sixteen bytes, then 256 instructions of four.
	out := make([]byte, 0, 128*16+256*4)

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

func filterGoldenPath(name string) string {
	return filepath.Join("testdata", "filters", name+".xz")
}

// TestGenerateFilterGolden rewrites the golden files with the XZ Utils
// binary on PATH. It runs only with XZ_UPDATE_FILTER_GOLDEN=1.
func TestGenerateFilterGolden(t *testing.T) {
	t.Parallel()

	if os.Getenv("XZ_UPDATE_FILTER_GOLDEN") != "1" {
		t.Skip("XZ_UPDATE_FILTER_GOLDEN is not 1")
	}

	bin, err := exec.LookPath("xz-utils")
	if err != nil {
		t.Fatal(err)
	}

	out, err := exec.CommandContext(t.Context(), bin, "--version").Output()
	if err != nil || !strings.HasPrefix(string(out), "xz (XZ Utils) ") {
		t.Fatalf("%s is not XZ Utils: %q, %v", bin, out, err)
	}

	input := filterInput()

	for _, tc := range filterCases() {
		// The chain is given in order, LZMA2 last.
		args := append(append([]string{"-c", "-T1"}, tc.args...), "--lzma2=preset=6")
		cmd := exec.CommandContext(t.Context(), bin, args...)

		cmd.Env = append(os.Environ(), "XZ_DEFAULTS=", "XZ_OPT=")
		cmd.Stdin = bytes.NewReader(input)

		var stderr bytes.Buffer

		cmd.Stderr = &stderr

		data, err := cmd.Output()
		if err != nil {
			t.Fatalf("xz %s: %v: %s", strings.Join(args, " "), err, stderr.String())
		}

		if err := os.WriteFile(filterGoldenPath(tc.name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFiltersAgainstXZUtils(t *testing.T) {
	t.Parallel()

	input := filterInput()

	for _, tc := range filterCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			data, err := os.ReadFile(filterGoldenPath(tc.name))
			if err != nil {
				t.Fatal(err)
			}

			r, err := xz.NewReader(bytes.NewReader(data))
			if err != nil {
				t.Fatalf("NewReader: %v", err)
			}

			out, err := io.ReadAll(r)
			if err != nil {
				t.Fatalf("Reader: %v", err)
			}

			if !bytes.Equal(out, input) {
				t.Fatalf(
					"Reader decodes %d bytes, differing from the input (first difference at %d)",
					len(out),
					firstDiff(out, input),
				)
			}

			pr, err := xz.NewReader(bytes.NewReader(data), xz.WithWorkers(4))
			if err != nil {
				t.Fatalf("NewParallelReader: %v", err)
			}

			out, err = io.ReadAll(pr)
			if cerr := pr.Close(); err == nil {
				err = cerr
			}

			if err != nil {
				t.Fatalf("ParallelReader: %v", err)
			}

			if !bytes.Equal(out, input) {
				t.Fatalf(
					"ParallelReader decodes %d bytes, differing from the input (first difference at %d)",
					len(out),
					firstDiff(out, input),
				)
			}
		})
	}
}

// firstDiff is the index of the first byte where a and b differ, or the
// shorter length.
func firstDiff(a, b []byte) int {
	n := min(len(a), len(b))
	for i := range n {
		if a[i] != b[i] {
			return i
		}
	}

	return n
}

// TestFiltersSmallReads decodes a golden file through reads of every small
// size: the BCJ reader's tail handling must not depend on how much the
// consumer asks for or how much the LZMA2 reader returns.
func TestFiltersSmallReads(t *testing.T) {
	t.Parallel()

	input := filterInput()

	for _, name := range []string{"x86", "riscv", "ia64", "x86-delta-arm"} {
		data, err := os.ReadFile(filterGoldenPath(name))
		if err != nil {
			t.Fatal(err)
		}

		for _, size := range []int{1, 3, 7, 64, 4097} {
			t.Run(fmt.Sprintf("%s/%d", name, size), func(t *testing.T) {
				t.Parallel()

				r, err := xz.NewReader(bytes.NewReader(data))
				if err != nil {
					t.Fatal(err)
				}

				var out []byte

				buf := make([]byte, size)

				for {
					n, err := r.Read(buf)
					out = append(out, buf[:n]...)

					if errors.Is(err, io.EOF) {
						break
					}

					if err != nil {
						t.Fatal(err)
					}
				}

				if !bytes.Equal(out, input) {
					t.Fatalf(
						"reads of %d bytes decode %d bytes, differing from the input at %d",
						size,
						len(out),
						firstDiff(out, input),
					)
				}
			})
		}
	}
}

// TestFiltersRefused covers the block headers a reader must refuse: a BCJ
// start offset its alignment does not divide, properties of the wrong
// size, and chains in the wrong order.
func TestFiltersRefused(t *testing.T) {
	t.Parallel()

	good, err := os.ReadFile(filterGoldenPath("arm-start"))
	if err != nil {
		t.Fatal(err)
	}

	// The block header starts after the 12-byte stream header: size byte,
	// flags, then the filter flags: ARM (0x07), 4 bytes of properties, the
	// start offset 4096 little endian.
	const hdr = 12

	off := bytes.Index(good[hdr:hdr+32], []byte{0x07, 0x04, 0x00, 0x10, 0x00, 0x00})
	if off < 0 {
		t.Fatal("the ARM filter flags are not where the header puts them")
	}

	off += hdr

	for _, tc := range []struct {
		name string
		// The patch replaces bytes at the given distance from the ARM
		// field: 0 is the ARM field itself, 6 the three-byte LZMA2 field
		// after it.
		at    int
		patch []byte
		kind  error
	}{
		{"misaligned start offset", 0, []byte{0x07, 0x04, 0x01, 0x10, 0x00, 0x00}, xz.ErrUnsupported},
		{"properties size 2", 0, []byte{0x07, 0x02, 0x00, 0x10, 0x00, 0x00}, xz.ErrCorrupt},
		// Delta in LZMA2's place: the chain ARM then delta, whose last
		// filter is not LZMA2.
		{"delta last", 6, []byte{0x03, 0x01, 0x00}, xz.ErrUnsupported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			data := bytes.Clone(good)
			copy(data[off+tc.at:], tc.patch)
			// The header's CRC32 no longer matches; a reader must report
			// the field, not the checksum, so the checksum is recomputed
			// by the helper the format tests share.
			fixBlockHeaderCRC(t, data[hdr:])

			_, err := xz.NewReader(bytes.NewReader(data))
			if err == nil {
				r, _ := xz.NewReader(bytes.NewReader(data))
				_, err = io.ReadAll(r)
			}

			if !errors.Is(err, tc.kind) {
				t.Fatalf("got %v, want %v", err, tc.kind)
			}
		})
	}
}

// fixBlockHeaderCRC recomputes the CRC32 of the block header at the start
// of data, whose size byte gives its length.
func fixBlockHeaderCRC(t *testing.T, data []byte) {
	t.Helper()

	n := (int(data[0]) + 1) * 4
	if n > len(data) {
		t.Fatalf("block header of %d bytes in %d", n, len(data))
	}

	binary.LittleEndian.PutUint32(data[n-4:], crc32.ChecksumIEEE(data[:n-4]))
}

// TestFiltersWriterAgainstXZUtils: "WithFilters applies filters before
// LZMA2, in the order given: up to three of the BCJ filters and Delta, as
// the format allows". The writer given the chain of a golden file states it
// in its block header as XZ Utils does, byte for byte: each filter's ID, a
// start offset only when there is one, the distance, LZMA2 last; and both
// readers decode its file to the input.
func TestFiltersWriterAgainstXZUtils(t *testing.T) {
	t.Parallel()

	input := filterInput()

	// The stream header is twelve bytes; the first block header follows,
	// its first byte its size.
	const hdr = 12

	for _, tc := range filterCases() {
		golden, err := os.ReadFile(filterGoldenPath(tc.name))
		if err != nil {
			t.Fatal(err)
		}

		opts := []xz.Option{xz.WithFilters(tc.chain...)}
		if tc.blockSize > 0 {
			opts = append(opts, xz.WithBlockSize(tc.blockSize))
		}

		var file bytes.Buffer

		w, err := xz.NewWriter(&file, opts...)
		if err != nil {
			t.Errorf("%s: NewWriter: %v", tc.name, err)

			continue
		}

		if _, err = w.Write(input); err != nil {
			t.Errorf("%s: Write: %v", tc.name, err)

			continue
		}

		if err = w.Close(); err != nil {
			t.Errorf("%s: Close: %v", tc.name, err)

			continue
		}

		want := golden[hdr : hdr+(int(golden[hdr])+1)*4]

		got := file.Bytes()[hdr:]
		if got = got[:min(len(got), len(want))]; !bytes.Equal(got, want) {
			t.Errorf("%s: the block header is % x, XZ Utils writes % x", tc.name, got, want)
		}

		sequential, parallel := both(file.Bytes())
		for name, read := range map[string]verdict{"sequential": sequential, "parallel": parallel} {
			if read.err != nil || !bytes.Equal(read.data, input) {
				t.Errorf("%s, %s: %d bytes, %v; first difference at %d",
					tc.name, name, len(read.data), read.err, firstDiff(read.data, input))
			}
		}
	}
}

// TestFiltersWriterPieces: the file a writer with filters writes is a
// function of the data, not of the writes that brought it: a byte at a
// time, in pieces shorter than an instruction, in pieces at and around the
// 32 KiB the BCJ writer converts at once, the file is the one a single
// write gives, and that one decodes to the data.
func TestFiltersWriterPieces(t *testing.T) {
	t.Parallel()

	// Twice the golden input: more than the BCJ writer holds at once.
	data := slices.Concat(filterInput(), filterInput())

	for name, chain := range map[string][]xz.Filter{
		"x86":           {{ID: xz.X86}},
		"x86-start":     {{ID: xz.X86, StartOffset: 1}},
		"arm64":         {{ID: xz.ARM64}},
		"ia64":          {{ID: xz.IA64}},
		"riscv":         {{ID: xz.RISCV}},
		"delta":         {{ID: xz.Delta, Distance: 3}},
		"x86-delta-arm": {{ID: xz.X86}, {ID: xz.Delta, Distance: 3}, {ID: xz.ARM}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			opts := []xz.Option{xz.WithFilters(chain...), xz.WithDictSize(lzma.MinDictSize)}
			whole := writeAll(t, opts, data)

			sequential, parallel := both(whole)
			for reader, got := range map[string]verdict{"sequential": sequential, "parallel": parallel} {
				if got.err != nil || !bytes.Equal(got.data, data) {
					t.Fatalf("one write, %s: %d bytes, %v; first difference at %d",
						reader, len(got.data), got.err, firstDiff(got.data, data))
				}
			}

			for _, size := range []int{1, 2, 3, 5, 7, 16, 4097, 32767, 32768, 32769, 40000} {
				var pieces [][]byte

				for rest := data; len(rest) > 0; rest = rest[min(size, len(rest)):] {
					pieces = append(pieces, rest[:min(size, len(rest))])
				}

				if got := writeAll(t, opts, pieces...); !bytes.Equal(got, whole) {
					t.Errorf("writes of %d bytes: a file of %d bytes, differing from the single write's %d at %d",
						size, len(got), len(whole), firstDiff(got, whole))
				}
			}
		})
	}
}

// TestFiltersPassErrorsOn: a filter changes nothing in how a reading ends.
// Through a BCJ filter, a Delta filter, a chain of three and a file of
// several blocks: an error of the source "is returned as it is", after
// bytes that are the start of the data and no other; a source that ends
// inside a block is io.ErrUnexpectedEOF; and an LZMA2 chunk the format
// does not have is ErrCorrupt. Each reading ends.
func TestFiltersPassErrorsOn(t *testing.T) {
	t.Parallel()

	input := filterInput()
	errSource := errors.New("source failed")

	// read reads src to its error through a buffer that holds, before
	// each Read, a byte the 16 KiB the input opens with have nowhere.
	read := func(src io.Reader, opts ...xz.Option) ([]byte, error) {
		r, err := xz.NewReader(src, opts...)
		if err != nil {
			return nil, err
		}

		defer func() { _ = r.Close() }()

		var out []byte

		buf := make([]byte, 4096)

		for {
			for i := range buf {
				buf[i] = 0xA5
			}

			n, err := r.Read(buf)
			out = append(out, buf[:n]...)

			if err != nil {
				return out, err
			}
		}
	}

	done := make(chan struct{})

	go func() {
		defer close(done)

		for _, name := range []string{"x86", "delta-4", "x86-delta-arm", "blocks-arm64"} {
			file, err := os.ReadFile(filterGoldenPath(name))
			if err != nil {
				t.Error(err)

				return
			}

			// The first LZMA2 chunk follows the first block header, and
			// its first byte says what it is: 0x03 to 0x7F say nothing.
			noChunk := bytes.Clone(file)
			noChunk[12+(int(file[12])+1)*4] = 0x7F

			for wname, workers := range map[string][]xz.Option{"in order": nil, "workers": {xz.WithWorkers(4)}} {
				got, err := read(&failing{file: file, n: len(file) / 2, err: errSource}, workers...)
				if !errors.Is(err, errSource) || errors.Is(err, xz.ErrCorrupt) {
					t.Errorf("%s, %s: the source fails: %v, want the source's error", name, wname, err)
				}

				if !bytes.HasPrefix(input, got) {
					t.Errorf("%s, %s: the source fails: the %d bytes read before it differ from the data at %d",
						name, wname, len(got), firstDiff(got, input))
				}

				_, err = read(&failing{file: file, n: len(file) / 2, err: io.EOF}, workers...)
				if !errors.Is(err, io.ErrUnexpectedEOF) {
					t.Errorf("%s, %s: the source ends: %v, want io.ErrUnexpectedEOF", name, wname, err)
				}

				if _, err = read(bytes.NewReader(noChunk), workers...); !errors.Is(err, xz.ErrCorrupt) {
					t.Errorf("%s, %s: no such chunk: %v, want ErrCorrupt", name, wname, err)
				}
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("a reader is still reading")
	}
}

// TestFiltersWriterPassesErrorsOn: "the first error from the underlying
// writer is sticky: Write and Close return it from then on and write
// nothing further", through a filter as without one. The Write that meets
// the sink's refusal returns the sink's error and a count within what it
// was given; the calls after it return the same error and take nothing;
// and the sink, which refused one write, sees no other.
func TestFiltersWriterPassesErrorsOn(t *testing.T) {
	t.Parallel()

	// Bytes that do not compress, so that the encoder writes while the
	// Write is still under way, and more of them than a filter holds.
	rng := rand.New(rand.NewPCG(0x6263, 0x6B))
	data := make([]byte, 256<<10)

	for i := range data {
		data[i] = byte(rng.Uint32())
	}

	for name, chain := range map[string][]xz.Filter{
		"x86":           {{ID: xz.X86}},
		"arm64":         {{ID: xz.ARM64}},
		"delta":         {{ID: xz.Delta, Distance: 3}},
		"x86-delta-arm": {{ID: xz.X86}, {ID: xz.Delta, Distance: 3}, {ID: xz.ARM}},
		"delta-x86":     {{ID: xz.Delta, Distance: 2}, {ID: xz.X86}},
	} {
		// The stream header and the block header go through; the block's
		// first bytes do not.
		s := &sink{allow: 2, err: errSinkFull}

		w, err := xz.NewWriter(s, xz.WithFilters(chain...), xz.WithDictSize(lzma.MinDictSize))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}

		n, err := w.Write(data)
		if !errors.Is(err, errSinkFull) {
			t.Errorf("%s: Write: %v, want the sink's error", name, err)
		}

		if n < 0 || n > len(data) {
			t.Errorf("%s: Write took %d bytes of %d", name, n, len(data))
		}

		if n, err = w.Write(data); n != 0 || !errors.Is(err, errSinkFull) {
			t.Errorf("%s: a Write after it: %d, %v, want 0 and the sink's error", name, n, err)
		}

		if err = w.Close(); !errors.Is(err, errSinkFull) {
			t.Errorf("%s: Close after it: %v, want the sink's error", name, err)
		}

		if s.after != 1 {
			t.Errorf("%s: the sink saw %d writes from its refusal on, want the one it refused", name, s.after)
		}
	}

	// A BCJ filter holds its last bytes until Close. With the 2 MiB of an
	// LZMA2 chunk, of a text that compresses to little, nothing of the
	// block has gone to the sink when Close comes, and the filter's last
	// bytes are what fills the chunk and sends it. The refusal they meet
	// is Close's error, and Close writes nothing after it.
	phrase := []byte("Sphinx of black quartz, judge my vow. ")
	text := bytes.Repeat(phrase, 1<<21/len(phrase)+1)[:1<<21]

	for name, chain := range map[string][]xz.Filter{
		"x86":       {{ID: xz.X86}},
		"delta-x86": {{ID: xz.Delta, Distance: 2}, {ID: xz.X86}},
	} {
		s := &sink{allow: 2, err: errSinkFull}

		w, err := xz.NewWriter(s, xz.WithFilters(chain...), xz.WithDictSize(lzma.MinDictSize))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}

		if n, werr := w.Write(text); n != len(text) || werr != nil {
			t.Errorf("%s: a Write the sink sees nothing of: %d, %v", name, n, werr)
		}

		if err = w.Close(); !errors.Is(err, errSinkFull) {
			t.Errorf("%s: Close: %v, want the sink's error", name, err)
		}

		if s.after != 1 {
			t.Errorf("%s: the sink saw %d writes from its refusal on, want the one it refused", name, s.after)
		}
	}
}

// rawChunks is data as uncompressed LZMA2 chunks of the sizes given, the
// first resetting the dictionary, and the end marker.
func rawChunks(data []byte, sizes ...int) []byte {
	var out []byte

	for i, size := range sizes {
		control := byte(0x02)
		if i == 0 {
			control = 0x01
		}

		out = append(out, control, byte((size-1)>>8), byte(size-1))
		out = append(out, data[:size]...)
		data = data[size:]
	}

	return append(out, 0x00)
}

// TestFiltersChunkBoundaries: what a BCJ reader makes of a block does not
// depend on where the LZMA2 chunks cut it. Two x86 calls of five bytes
// each, to the instruction that follows them, which the filter stores as
// the addresses 5 and 10: in one chunk, in two cut at every byte, the last
// a single byte included, and in a chunk a byte, both readers decode the
// two calls.
func TestFiltersChunkBoundaries(t *testing.T) {
	t.Parallel()

	data := []byte{0xE8, 0x00, 0x00, 0x00, 0x00, 0xE8, 0x00, 0x00, 0x00, 0x00}
	filtered := []byte{0xE8, 0x05, 0x00, 0x00, 0x00, 0xE8, 0x0A, 0x00, 0x00, 0x00}

	cuts := map[string][]int{
		"one chunk":      {len(filtered)},
		"a chunk a byte": slices.Repeat([]int{1}, len(filtered)),
	}

	for at := 1; at < len(filtered); at++ {
		cuts[fmt.Sprintf("two chunks cut at %d", at)] = []int{at, len(filtered) - at}
	}

	// The x86 filter, without properties, before LZMA2.
	o := refOptions{check: flagCRC32, dictCode: 0, before: [][]byte{{0x04, 0x00}}}

	for name, sizes := range cuts {
		block := refBlock{data: data, lzma2: rawChunks(filtered, sizes...)}

		// Alone, a block is read in order; two are read by offset.
		for _, blocks := range [][]refBlock{{block}, {block, block}} {
			want := bytes.Repeat(data, len(blocks))

			sequential, parallel := both(refStream(o, blocks))
			for reader, got := range map[string]verdict{"sequential": sequential, "parallel": parallel} {
				if got.err != nil || !bytes.Equal(got.data, want) {
					t.Errorf("%s, %d blocks, %s: % x, %v; want % x",
						name, len(blocks), reader, got.data, got.err, want)
				}
			}
		}
	}
}

// Copyright 2026 Forkcloser. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The bounded tests walk the implementation's boundaries: the writer's
// block cuts at and around its block size across several writes, the
// parallel reader's block buffer at and around its initial size and its
// worker count against the block count, read sizes around the readers'
// buffers, and a file's streams with different check types.

package xz_test

import (
	"bytes"
	"errors"
	"hash/crc32"
	"io"
	"math/rand/v2"
	"runtime"
	"sync/atomic"
	"testing"

	"github.com/forkcloser/xz/v2"
	"github.com/forkcloser/xz/v2/lzma"
)

// writeAll writes data through w in the given pieces and closes it.
func writeAll(tb testing.TB, opts []xz.Option, pieces ...[]byte) []byte {
	tb.Helper()

	var buf bytes.Buffer

	w, err := xz.NewWriter(&buf, opts...)
	if err != nil {
		tb.Fatal(err)
	}

	for _, p := range pieces {
		if _, err = w.Write(p); err != nil {
			tb.Fatal(err)
		}
	}

	if err = w.Close(); err != nil {
		tb.Fatal(err)
	}

	return buf.Bytes()
}

// TestBoundedWriterBlocks: the writer cuts blocks at its block size however
// the writes fall: a write of the size, one past it, two of the size, a
// zero-length write at a boundary, an empty input, and writes that cross
// the boundary; the reference parses each and counts the blocks.
func TestBoundedWriterBlocks(t *testing.T) {
	t.Parallel()

	const size = 1024

	data := cData(3 * size)
	c := []xz.Option{xz.WithBlockSize(size)}

	cases := []struct {
		name   string
		pieces [][]byte
		blocks int
	}{
		{"empty", [][]byte{{}}, 1},
		{"one byte", [][]byte{data[:1]}, 1},
		{"the size", [][]byte{data[:size]}, 1},
		{"one past", [][]byte{data[:size+1]}, 2},
		{"twice the size", [][]byte{data[:size], data[size : 2*size]}, 2},
		{"zero-length at the boundary", [][]byte{data[:size], {}}, 1},
		{"crossing", [][]byte{data[:1000], data[1000:1048]}, 2},
		{"three and a bit", [][]byte{data[:3*size], data[:5]}, 4},
	}

	for _, tc := range cases {
		var want []byte
		for _, p := range tc.pieces {
			want = append(want, p...)
		}

		file := writeAll(t, c, tc.pieces...)

		got, err := refParse(file)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("%s: the reference: %d bytes, %v", tc.name, len(got), err)
		}

		if blocks := refCountBlocks(file); blocks != tc.blocks {
			t.Fatalf("%s: %d blocks, want %d", tc.name, blocks, tc.blocks)
		}

		sequential, parallel := both(file)
		if sequential.err != nil || !bytes.Equal(sequential.data, want) || parallel.err != nil ||
			!bytes.Equal(parallel.data, want) {
			t.Fatalf(
				"%s: sequential %d bytes %v; parallel %d bytes %v",
				tc.name,
				len(sequential.data),
				sequential.err,
				len(parallel.data),
				parallel.err,
			)
		}
	}
}

// TestBoundedParallelBlocks: the parallel reader decodes blocks of every
// size around its initial block buffer (1 MiB), with every worker count
// against every block count, and by reads of one byte, a prime and a whole
// buffer, to the same bytes as the reference.
func TestBoundedParallelBlocks(t *testing.T) {
	t.Parallel()

	const initial = 1 << 20

	for _, blockSize := range []int64{initial - 1, initial, initial + 1, 2*initial + 1} {
		data := cData(int(2*blockSize) + 3)
		file := writeAll(t, []xz.Option{xz.WithBlockSize(blockSize)}, data)

		for _, workers := range []int{1, 2, 5} {
			r, err := xz.NewReader(bytes.NewReader(file), xz.WithWorkers(workers))
			if err != nil {
				t.Fatal(err)
			}

			got, err := io.ReadAll(r)
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("block %d, %d workers: %d bytes, %v", blockSize, workers, len(got), err)
			}

			if err = r.Close(); err != nil {
				t.Fatal(err)
			}
		}
	}

	data := cData(100000)
	file := writeAll(t, []xz.Option{xz.WithBlockSize(4096)}, data)

	for _, size := range []int{1, 7, 4096, 1 << 20} {
		r, err := xz.NewReader(bytes.NewReader(file), xz.WithWorkers(3))
		if err != nil {
			t.Fatal(err)
		}

		got, err := readBy(r, size)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("reads of %d: %d bytes, %v", size, len(got), err)
		}

		sr, err := xz.NewReader(bytes.NewReader(file))
		if err != nil {
			t.Fatal(err)
		}

		got, err = readBy(sr, size)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("sequential reads of %d: %d bytes, %v", size, len(got), err)
		}
	}
}

// readBy reads r to the end in pieces of size bytes.
func readBy(r io.Reader, size int) ([]byte, error) {
	var out []byte

	buf := make([]byte, size)

	for {
		n, err := r.Read(buf)
		out = append(out, buf[:n]...)

		if err != nil {
			if err == io.EOF {
				return out, nil
			}

			return out, err
		}
	}
}

// TestBoundedStreams: a file of several streams, each with its own check
// type and block layout, with and without padding, decodes whole through
// both readers, and SingleStream stops after the first.
func TestBoundedStreams(t *testing.T) {
	t.Parallel()

	parts := [][]byte{cData(5000), cData(1), {}, cData(70000)}
	streams := make([][]byte, 0, len(parts))

	for i, part := range parts {
		check := []byte{flagNone, flagCRC32, flagCRC64, flagSHA256}[i]
		streams = append(
			streams,
			refStream(refOptions{check: check, sizes: i%2 == 0, dictCode: 30}, refBlocks(t, part, 3000)),
		)
	}

	for _, padding := range []int{0, 4, 12} {
		file := refFile(padding, streams...)

		want, err := refParse(file)
		if err != nil {
			t.Fatal(err)
		}

		sequential, parallel := both(file)
		if sequential.err != nil || !bytes.Equal(sequential.data, want) {
			t.Fatalf("padding %d: sequential: %d bytes, %v", padding, len(sequential.data), sequential.err)
		}

		if parallel.err != nil || !bytes.Equal(parallel.data, want) {
			t.Fatalf("padding %d: parallel: %d bytes, %v", padding, len(parallel.data), parallel.err)
		}

		r, err := xz.NewReader(bytes.NewReader(file), xz.WithSingleStream())
		if err != nil {
			t.Fatal(err)
		}

		got, err := io.ReadAll(r)
		if !bytes.Equal(got, parts[0]) || (err == nil) != (padding == 0 && len(streams) == 1) {
			t.Fatalf(
				"padding %d, single stream: %d bytes, %v; want the first stream then a refusal of what follows",
				padding,
				len(got),
				err,
			)
		}
	}
}

// TestBoundedWriterSmallBlocks: a block's encoder is sized by the block
// when the block is shorter than the dictionary. A megabyte in blocks of
// 4 KiB at the default dictionary allocates within 128 MiB, where it
// measures under 40 and an encoder at the full 8 MiB dictionary for each
// of the 256 blocks would be 12 GiB. The block headers still state the
// dictionary asked for, and the file is the data.
//
//nolint:paralleltest // measures the process's TotalAlloc against a budget; a parallel test's allocations would count against it
func TestBoundedWriterSmallBlocks(t *testing.T) {
	const budget = 128 << 20

	data := cData(1 << 20)
	whole := writeAll(t, nil, data)

	for name, opts := range map[string][]xz.Option{
		"no workers": {xz.WithBlockSize(4 << 10)},
		"workers":    {xz.WithBlockSize(4 << 10), xz.WithWorkers(2)},
	} {
		var before, after runtime.MemStats

		runtime.ReadMemStats(&before)

		file := writeAll(t, opts, data)

		runtime.ReadMemStats(&after)

		if used := after.TotalAlloc - before.TotalAlloc; used > budget {
			t.Errorf("%s: %d bytes allocated for 256 blocks of 4 KiB; budget is %d", name, used, budget)
		}

		// The stream header is twelve bytes; the block header that follows
		// is its size, its flags, the LZMA2 filter's ID and properties
		// size, and the dictionary's code.
		if file[16] != whole[16] {
			t.Errorf("%s: the block header states dictionary code %d, want the default's %d", name, file[16], whole[16])
		}

		if got, err := refParse(file); err != nil || !bytes.Equal(got, data) {
			t.Errorf("%s: the reference reads %d bytes, %v", name, len(got), err)
		}
	}
}

// TestBoundedBlockEncoderSeesItsBlock: an encoder sized by its block still
// finds what the block holds. 40,000 random bytes and then the first 25,000
// of them again, in a block of 64 KiB under the default dictionary, are
// written in little more than the 40,000: the copy lies 40,000 bytes back,
// which an encoder with a dictionary shorter than the block would not see.
func TestBoundedBlockEncoderSeesItsBlock(t *testing.T) {
	t.Parallel()

	rng := rand.New(rand.NewPCG(3, 9))
	random := make([]byte, 40000)

	for i := range random {
		random[i] = byte(rng.Uint32())
	}

	data := append(bytes.Clone(random), random[:25000]...)

	for name, opts := range map[string][]xz.Option{
		"no workers": {xz.WithBlockSize(64 << 10)},
		"workers":    {xz.WithBlockSize(64 << 10), xz.WithWorkers(2)},
	} {
		file := writeAll(t, opts, data)
		if len(file) > 45000 {
			t.Errorf("%s: %d bytes written for 40000 random bytes and a copy of 25000 of them", name, len(file))
		}

		if got, err := refParse(file); err != nil || !bytes.Equal(got, data) {
			t.Errorf("%s: the reference reads %d bytes, %v", name, len(got), err)
		}
	}
}

// TestBoundedWriterSizes: the writer's output for inputs around the
// encoder's buffers, in one write, parses under the reference to the
// input.
func TestBoundedWriterSizes(t *testing.T) {
	t.Parallel()

	for _, n := range []int{0, 1, 4095, 4096, 4097, 65535, 65536, 65537, 1<<21 - 1, 1 << 21, 1<<21 + 1} {
		data := cData(n)

		for _, opts := range [][]xz.Option{nil, {xz.WithProperties(0, 2, 0)}, {xz.WithDictSize(lzma.MinDictSize)}} {
			got, err := refParse(writeAll(t, opts, data))
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("len %d: the reference: %d bytes, %v", n, len(got), err)
			}
		}
	}
}

// TestBoundedSingleStreamReadsNoFurther: a reader of one stream reads no
// further than that stream and the byte that tells it something follows.
// Ten kilobytes after the stream, in a bytes.Buffer the reader consumes
// from: when the reader has refused the first of them, the buffer still
// holds all the others. A reader of every stream reads ahead; this one
// must not.
func TestBoundedSingleStreamReadsNoFurther(t *testing.T) {
	t.Parallel()

	data := cData(2000)
	stream := refStream(refOptions{check: flagCRC32, dictCode: 20}, refBlocks(t, data, 1000))

	const after = 10 << 10

	in := bytes.NewBuffer(append(bytes.Clone(stream), make([]byte, after)...))

	r, err := xz.NewReader(in, xz.WithSingleStream())
	if err != nil {
		t.Fatal(err)
	}

	got, err := io.ReadAll(r)
	if !errors.Is(err, xz.ErrCorrupt) || !bytes.Equal(got, data) {
		t.Fatalf("%d bytes, %v", len(got), err)
	}

	if in.Len() != after-1 {
		t.Errorf("%d of the %d bytes after the stream are left in the input, want all but one", in.Len(), after)
	}
}

// TestBoundedReaderDictionaryAcrossBlocks: every block of a file starts a
// dictionary of its own, and the reader that decodes them one after the
// other holds what one block needs, not what they add up to. 64 blocks of
// 256 KiB under a dictionary size of 64 MiB decode within 4 MiB of
// allocations: before the dictionary restarted its buffer at each block,
// the same file cost 33 MiB, the buffer doubling toward the file's 16.
//
//nolint:paralleltest // measures the process's TotalAlloc against a budget; a parallel test's allocations would count against it
func TestBoundedReaderDictionaryAcrossBlocks(t *testing.T) {
	const (
		blocks    = 64
		blockSize = 256 << 10
		budget    = 4 << 20
	)

	data := make([]byte, blocks*blockSize)

	rng := rand.New(rand.NewPCG(1, 2))
	for i := range data {
		data[i] = byte(rng.IntN(256))
	}

	var file bytes.Buffer

	w, err := xz.NewWriter(&file, xz.WithDictSize(64<<20), xz.WithBlockSize(blockSize))
	if err != nil {
		t.Fatal(err)
	}

	if _, err = w.Write(data); err != nil {
		t.Fatal(err)
	}

	if err = w.Close(); err != nil {
		t.Fatal(err)
	}

	var before, after runtime.MemStats

	//revive:disable-next-line:call-to-gc the allocation budget is measured from a collected heap
	runtime.GC()
	runtime.ReadMemStats(&before)

	r, err := xz.NewReader(bytes.NewReader(file.Bytes()))
	if err != nil {
		t.Fatal(err)
	}

	sum := crc32.NewIEEE()

	n, err := io.Copy(sum, r)

	runtime.ReadMemStats(&after)

	if err != nil || n != int64(len(data)) || sum.Sum32() != crc32.ChecksumIEEE(data) {
		t.Fatalf("decoded %d bytes, %v; want the %d written", n, err, len(data))
	}

	if used := after.TotalAlloc - before.TotalAlloc; used > budget {
		t.Errorf("decoding %d blocks of %d bytes allocated %d bytes; budget is %d", blocks, blockSize, used, budget)
	}
}

// meter is a file that counts what is asked of it: the Read calls of a
// reader in order, the bytes a reader with workers fetches by offset.
type meter struct {
	*bytes.Reader

	reads int
	// ReadAt is called from every worker.
	fetches, fetched atomic.Int64
}

func (m *meter) Read(p []byte) (int, error) {
	m.reads++

	return m.Reader.Read(p)
}

func (m *meter) ReadAt(p []byte, off int64) (int, error) {
	n, err := m.Reader.ReadAt(p, off)
	m.fetches.Add(1)
	m.fetched.Add(int64(n))

	return n, err
}

// inOrder hides everything of the meter but Read, so a reader with or
// without workers reads it in order.
type inOrder struct{ m *meter }

func (o inOrder) Read(p []byte) (int, error) { return o.m.Read(p) }

// TestBoundedReadCost: opening and reading a file costs in proportion to
// the file. Stream padding is legal at any length and a stream may be
// empty, so a megabyte of padding and twenty thousand empty streams are
// both valid files a few bytes describe. Read in order, the padding is at
// most a Read call a kilobyte, where it was one for every four bytes; with
// workers, what is fetched by offset is within four times the file, where
// a buffer's worth was fetched for every stream, a thousand times the file,
// and the padding is found in a fetch a kilobyte at most, not one a group.
func TestBoundedReadCost(t *testing.T) {
	t.Parallel()

	data := cData(2000)
	o := refOptions{check: flagCRC32, dictCode: 20}

	padded := refFile(
		1<<20,
		refStream(o, refBlocks(t, data[:1000], 1000)),
		refStream(o, refBlocks(t, data[1000:], 1000)),
	)

	empty := refStream(o, nil)
	streams := bytes.Repeat(empty, 20000)

	for _, tc := range []struct {
		name    string
		file    []byte
		want    []byte
		streams int
	}{
		{"a megabyte of padding, twice", padded, data, 2},
		{"twenty thousand empty streams", streams, nil, 20000},
	} {
		t.Run(tc.name+", in order", func(t *testing.T) {
			t.Parallel()

			m := &meter{Reader: bytes.NewReader(tc.file)}

			r, err := xz.NewReader(inOrder{m})
			if err != nil {
				t.Fatal(err)
			}

			if got, rerr := io.ReadAll(r); rerr != nil || !bytes.Equal(got, tc.want) {
				t.Fatalf("%d bytes, %v", len(got), rerr)
			}

			if limit := len(tc.file)/1024 + 16; m.reads > limit {
				t.Errorf("%d Read calls for a file of %d bytes, want at most %d", m.reads, len(tc.file), limit)
			}
		})

		t.Run(tc.name+", with workers", func(t *testing.T) {
			t.Parallel()

			m := &meter{Reader: bytes.NewReader(tc.file)}

			r, err := xz.NewReader(m, xz.WithWorkers(4))
			if err != nil {
				t.Fatal(err)
			}

			// Opening is where the streams and the padding between them
			// are found; a stream of its own costs a few fetches.
			if limit := int64(len(tc.file)/1024 + 64 + 8*tc.streams); m.fetches.Load() > limit {
				t.Errorf(
					"%d fetches to open a file of %d bytes, want at most %d",
					m.fetches.Load(), len(tc.file), limit,
				)
			}

			got, rerr := io.ReadAll(r)
			if rerr != nil || !bytes.Equal(got, tc.want) {
				t.Fatalf("%d bytes, %v", len(got), rerr)
			}

			if cerr := r.Close(); cerr != nil {
				t.Fatal(cerr)
			}

			if limit := 4 * int64(len(tc.file)); m.fetched.Load() > limit {
				t.Errorf("%d bytes fetched for a file of %d, want at most %d", m.fetched.Load(), len(tc.file), limit)
			}
		})
	}
}

// Copyright 2026 Forkcloser. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The parallel writer's contract, from WithWorkers' documentation: "The
// file is the one a writer without workers writes at that block size, byte
// for byte, whatever the number of workers." The sequential writer is the
// reference, and the interop tests hold both to XZ Utils.

package xz_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"runtime"
	"testing"
	"time"

	"github.com/forkcloser/xz/v2"
	"github.com/forkcloser/xz/v2/lzma"
)

// compressible returns n bytes that LZMA shortens, the same for a seed.
func compressible(n int, seed uint64) []byte {
	data := make([]byte, n)
	rng := rand.New(rand.NewPCG(seed, 17))

	for i := range data {
		data[i] = byte(rng.IntN(7))
	}

	return data
}

// written compresses data through a writer with the options, in pieces of
// the given size (0: one Write).
func written(t *testing.T, data []byte, piece int, opts ...xz.Option) []byte {
	t.Helper()

	var file bytes.Buffer

	w, err := xz.NewWriter(&file, opts...)
	if err != nil {
		t.Fatal(err)
	}

	if piece == 0 {
		piece = len(data) + 1
	}

	for rest := data; len(rest) > 0; {
		k := min(piece, len(rest))

		if n, werr := w.Write(rest[:k]); werr != nil || n != k {
			t.Fatalf("Write: %d of %d, %v", n, k, werr)
		}

		rest = rest[k:]
	}

	if err = w.Close(); err != nil {
		t.Fatal(err)
	}

	return file.Bytes()
}

// TestContractWriterWithWorkersWritesTheSameFile: at every size around a
// block's, in one Write and in pieces that straddle the blocks, with one
// worker's worth of blocks out or eight, the file is the sequential
// writer's; and it reads back, in order and through the parallel reader.
func TestContractWriterWithWorkersWritesTheSameFile(t *testing.T) {
	t.Parallel()

	const block = 2 << 10

	for _, size := range []int{0, 1, block - 1, block, block + 1, 2 * block, 5*block + 123, 24 * block} {
		data := compressible(size, uint64(size))
		want := written(t, data, 0, xz.WithBlockSize(block))

		for _, workers := range []int{0, 2, 8} {
			for _, piece := range []int{0, 700, block + 1} {
				t.Run(fmt.Sprintf("%d bytes, %d workers, pieces of %d", size, workers, piece), func(t *testing.T) {
					t.Parallel()

					got := written(t, data, piece, xz.WithBlockSize(block), xz.WithWorkers(workers))
					if !bytes.Equal(got, want) {
						t.Fatalf("a file of %d bytes, not the sequential writer's %d", len(got), len(want))
					}

					for _, readers := range []int{1, 4} {
						r, err := xz.NewReader(bytes.NewReader(got), xz.WithWorkers(readers))
						if err != nil {
							t.Fatal(err)
						}

						back, rerr := io.ReadAll(r)
						if err = errors.Join(rerr, r.Close()); err != nil || !bytes.Equal(back, data) {
							t.Errorf(
								"read back with %d workers: %d bytes, %v; want the %d written",
								readers,
								len(back),
								err,
								size,
							)
						}
					}
				})
			}
		}
	}
}

// TestContractWriterWithWorkersBlockSize: "without it of three times the
// dictionary size and at least 1 MiB". The file is the sequential writer's
// at that block size, for a dictionary below a third of a mebibyte and for
// one above; and one worker is no workers: a single block.
func TestContractWriterWithWorkersBlockSize(t *testing.T) {
	t.Parallel()

	// More than two blocks at either size.
	data := compressible(3<<20+77, 5)

	for _, tc := range []struct {
		name  string
		dict  int
		block int64
	}{
		{"the smallest dictionary", lzma.MinDictSize, 1 << 20},
		{"a dictionary of 512 KiB", 512 << 10, 3 * 512 << 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := written(t, data, 64<<10, xz.WithDictSize(tc.dict), xz.WithWorkers(2))
			want := written(t, data, 0, xz.WithDictSize(tc.dict), xz.WithBlockSize(tc.block))

			if !bytes.Equal(got, want) {
				t.Errorf(
					"a file of %d bytes, not the sequential writer's %d at blocks of %d",
					len(got),
					len(want),
					tc.block,
				)
			}
		})
	}

	t.Run("one worker", func(t *testing.T) {
		t.Parallel()

		got := written(t, data, 64<<10, xz.WithDictSize(lzma.MinDictSize), xz.WithWorkers(1))
		want := written(t, data, 0, xz.WithDictSize(lzma.MinDictSize))

		if !bytes.Equal(got, want) {
			t.Errorf("a file of %d bytes, not the %d of a writer given no workers", len(got), len(want))
		}
	})
}

// sink is an underlying writer that fails from its nth Write on, and
// counts the calls made after the first failure.
type sink struct {
	allow, calls, after int
	err                 error
	file                bytes.Buffer
}

func (s *sink) Write(p []byte) (int, error) {
	s.calls++
	if s.calls > s.allow {
		s.after++

		return 0, s.err
	}

	return s.file.Write(p)
}

var errSinkFull = errors.New("the sink is full")

// TestContractWriterWithWorkersErrorIsSticky: "an error from the
// underlying writer is returned by the Write or Close that meets it". With
// the stream header written and the first block's write refused, the Write
// or Close that made that one write returns the sink's error, every call
// after it returns the same, and nothing more is written: the sink refuses
// one write and sees no other.
func TestContractWriterWithWorkersErrorIsSticky(t *testing.T) {
	t.Parallel()

	const block = 4 << 10

	for _, blocks := range []int{1, 2, 3, 12} {
		t.Run(fmt.Sprintf("%d blocks", blocks), func(t *testing.T) {
			t.Parallel()

			// The stream header is the first write.
			s := &sink{allow: 1, err: errSinkFull}

			w, err := xz.NewWriter(s, xz.WithBlockSize(block), xz.WithWorkers(2))
			if err != nil {
				t.Fatal(err)
			}

			var first error

			for range blocks {
				if _, werr := w.Write(compressible(block, 9)); werr != nil {
					first = werr

					break
				}
			}

			if first == nil {
				first = w.Close()
			}

			if !errors.Is(first, errSinkFull) {
				t.Fatalf("the first failure is %v, want the sink's", first)
			}

			if s.after != 1 {
				t.Errorf("the sink refused %d writes before its error was returned, want 1", s.after)
			}

			after := s.after

			if _, werr := w.Write([]byte("more")); !errors.Is(werr, errSinkFull) {
				t.Errorf("Write after the failure: %v, want the sink's error", werr)
			}

			if cerr := w.Close(); !errors.Is(cerr, errSinkFull) {
				t.Errorf("Close after the failure: %v, want the sink's error", cerr)
			}

			if s.after != after {
				t.Errorf("%d writes reached the sink after its first refusal was reported", s.after-after)
			}
		})
	}
}

// TestContractWriterWithWorkersEncodesThatManyAtOnce: "encodes that many
// at once". With four workers, four whole blocks are handed over and none
// is written: the sink has seen the stream header alone. The fifth is what
// makes the writer wait for the oldest and write it, one block and no more.
func TestContractWriterWithWorkersEncodesThatManyAtOnce(t *testing.T) {
	t.Parallel()

	const (
		block   = 4 << 10
		workers = 4
	)

	s := &sink{allow: math.MaxInt}

	w, err := xz.NewWriter(s, xz.WithBlockSize(block), xz.WithWorkers(workers))
	if err != nil {
		t.Fatal(err)
	}

	if s.calls != 1 {
		t.Fatalf("%d writes reached the sink from the constructor, want the stream header's 1", s.calls)
	}

	// A byte into the fifth block, so the fourth is handed over.
	if _, err = w.Write(compressible(workers*block+1, 11)); err != nil {
		t.Fatal(err)
	}

	if s.calls != 1 {
		t.Errorf(
			"%d blocks were written with %d handed over to %d workers, want none",
			s.calls-1, workers, workers,
		)
	}

	if _, err = w.Write(compressible(block, 12)); err != nil {
		t.Fatal(err)
	}

	if s.calls != 2 {
		t.Errorf("%d blocks were written when a fifth was handed over to %d workers, want 1", s.calls-1, workers)
	}

	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestContractWriterWithWorkersOnOneCPU: "whatever the number of workers".
// WithWorkers(0) on a machine with one CPU comes to one worker, and still
// cuts the blocks a machine with eighteen cuts: a file is not a function of
// where it was made.
//
//nolint:paralleltest // sets the process's GOMAXPROCS; a parallel test would run under it
func TestContractWriterWithWorkersOnOneCPU(t *testing.T) {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))

	data := compressible(2<<20+77, 13)

	got := written(t, data, 64<<10, xz.WithDictSize(lzma.MinDictSize), xz.WithWorkers(0))
	want := written(t, data, 0, xz.WithDictSize(lzma.MinDictSize), xz.WithBlockSize(1<<20))

	if !bytes.Equal(got, want) {
		t.Errorf("a file of %d bytes on one CPU, not the %d of blocks of 1 MiB", len(got), len(want))
	}
}

// TestContractWorkersLeaveGOMAXPROCS: WithWorkers takes "0 for GOMAXPROCS":
// the constructors read the setting and leave it as it is.
//
//nolint:paralleltest // sets the process's GOMAXPROCS; a parallel test would run under it
func TestContractWorkersLeaveGOMAXPROCS(t *testing.T) {
	// Two, so that the setting has somewhere to fall to on any machine.
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(2))

	file := written(t, compressible(8<<10, 5), 0, xz.WithBlockSize(4<<10))

	if _, err := xz.NewWriter(io.Discard, xz.WithWorkers(0)); err != nil {
		t.Fatal(err)
	}

	r, err := xz.NewReader(bytes.NewReader(file), xz.WithWorkers(0))
	if err != nil {
		t.Fatal(err)
	}

	if err = r.Close(); err != nil {
		t.Fatal(err)
	}

	if got := runtime.GOMAXPROCS(0); got != 2 {
		t.Errorf("GOMAXPROCS is %d after WithWorkers(0), want the 2 it was", got)
	}
}

// TestContractWriterWithWorkersClosed: a writer with workers is closed
// once, like any: a second Close and a Write after Close match ErrClosed
// and add nothing to the file, and the Write takes none of its bytes.
func TestContractWriterWithWorkersClosed(t *testing.T) {
	t.Parallel()

	var file bytes.Buffer

	w, err := xz.NewWriter(&file, xz.WithBlockSize(4<<10), xz.WithWorkers(4))
	if err != nil {
		t.Fatal(err)
	}

	if _, err = w.Write(compressible(30<<10, 3)); err != nil {
		t.Fatal(err)
	}

	if err = w.Close(); err != nil {
		t.Fatal(err)
	}

	size := file.Len()

	if n, werr := w.Write([]byte("more")); n != 0 || !errors.Is(werr, xz.ErrClosed) {
		t.Errorf("Write after Close: %d, %v, want 0 and ErrClosed", n, werr)
	}

	if cerr := w.Close(); !errors.Is(cerr, xz.ErrClosed) {
		t.Errorf("a second Close: %v, want ErrClosed", cerr)
	}

	if file.Len() != size {
		t.Errorf("%d bytes written after Close", file.Len()-size)
	}
}

// TestContractWriterWithWorkersRefusesNegative: a negative count is refused
// by the constructor, as the reader's is.
func TestContractWriterWithWorkersRefusesNegative(t *testing.T) {
	t.Parallel()

	if _, err := xz.NewWriter(io.Discard, xz.WithWorkers(-1)); err == nil {
		t.Error("NewWriter took -1 workers")
	}
}

// TestContractWriterWithWorkersAbandoned: a writer with workers that is
// dropped without Close leaves no goroutine behind: its encoders end with
// their blocks. The reader's workers need a Close; these do not.
//
//nolint:paralleltest // counts the process's goroutines; a parallel test's would be counted
func TestContractWriterWithWorkersAbandoned(t *testing.T) {
	before := runtime.NumGoroutine()

	for range 8 {
		w, err := xz.NewWriter(io.Discard, xz.WithBlockSize(4<<10), xz.WithWorkers(4))
		if err != nil {
			t.Fatal(err)
		}

		if _, err = w.Write(compressible(64<<10, 4)); err != nil {
			t.Fatal(err)
		}
	}

	deadline := time.Now().Add(10 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	if now := runtime.NumGoroutine(); now > before {
		t.Errorf("%d goroutines where there were %d before the abandoned writers", now, before)
	}
}

// TestBoundedWriterWithWorkersSmallWrites: a block filled a thousand bytes
// at a time is not copied a thousand times. 2 MiB in blocks of 1 MiB
// allocate within 32 MiB, where they measure 8; a block's buffer grown to
// exactly what each Write adds allocated 1.1 GiB.
//
//nolint:paralleltest // measures the process's TotalAlloc against a budget; a parallel test's allocations would count against it
func TestBoundedWriterWithWorkersSmallWrites(t *testing.T) {
	const budget = 32 << 20

	data := compressible(2<<20, 12)

	var before, after runtime.MemStats

	//revive:disable-next-line:call-to-gc the allocation budget is measured from a collected heap
	runtime.GC()
	runtime.ReadMemStats(&before)

	w, err := xz.NewWriter(io.Discard, xz.WithBlockSize(1<<20), xz.WithWorkers(2), xz.WithDictSize(lzma.MinDictSize))
	if err != nil {
		t.Fatal(err)
	}

	for rest := data; len(rest) > 0; {
		k := min(1000, len(rest))

		if _, err = w.Write(rest[:k]); err != nil {
			t.Fatal(err)
		}

		rest = rest[k:]
	}

	if err = w.Close(); err != nil {
		t.Fatal(err)
	}

	runtime.ReadMemStats(&after)

	if used := after.TotalAlloc - before.TotalAlloc; used > budget {
		t.Errorf("2 MiB in writes of 1000 bytes allocated %d bytes; budget is %d", used, budget)
	}
}

// heapSink discards what it is given and records the largest live heap it
// sees, measured after a collection, at each Write.
type heapSink struct {
	peak uint64
}

func (s *heapSink) Write(p []byte) (int, error) {
	var m runtime.MemStats

	//revive:disable-next-line:call-to-gc the live heap is what is measured
	runtime.GC()
	runtime.ReadMemStats(&m)

	s.peak = max(s.peak, m.HeapAlloc)

	return len(p), nil
}

// TestBoundedWriterWithWorkersMemory: "a writer with n workers needs about
// n times twice the block size, and its encoders". 16 MiB written through
// two workers in blocks of 256 KiB, a block's worth at a time, never holds
// more than 8 MiB live above what the test itself holds, where it measures
// under 3: the blocks out at once are bounded by the workers, not by the
// data, whose 64 blocks would be 32 MiB with what they compress to.
//
//nolint:paralleltest // measures the process's live heap; a parallel test's allocations would count
func TestBoundedWriterWithWorkersMemory(t *testing.T) {
	const (
		block  = 256 << 10
		total  = 16 << 20
		budget = 8 << 20
	)

	data := make([]byte, total)

	rng := rand.New(rand.NewPCG(8, 9))
	for i := range data {
		data[i] = byte(rng.IntN(256))
	}

	var base runtime.MemStats

	//revive:disable-next-line:call-to-gc the baseline is the live heap
	runtime.GC()
	runtime.ReadMemStats(&base)

	s := &heapSink{}

	w, err := xz.NewWriter(s, xz.WithBlockSize(block), xz.WithWorkers(2), xz.WithDictSize(64<<10))
	if err != nil {
		t.Fatal(err)
	}

	for rest := data; len(rest) > 0; rest = rest[block:] {
		if _, err = w.Write(rest[:block]); err != nil {
			t.Fatal(err)
		}
	}

	if err = w.Close(); err != nil {
		t.Fatal(err)
	}

	if s.peak > base.HeapAlloc+budget {
		t.Errorf("the live heap reached %d bytes above the test's own; budget is %d", s.peak-base.HeapAlloc, budget)
	}

	runtime.KeepAlive(data)
}

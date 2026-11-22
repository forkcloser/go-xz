// Copyright 2026 Forkcloser. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package xz_test

import (
	"bytes"
	"io"
	"runtime"
	"testing"

	"github.com/forkcloser/xz/v2"
)

// zeroBlocks is an xz file of count blocks, each of size zero bytes: a few
// kilobytes that decode to count times size.
func zeroBlocks(t *testing.T, count int, size int64) []byte {
	t.Helper()

	var file bytes.Buffer

	writer, err := xz.NewWriter(&file, xz.WithBlockSize(size))
	if err != nil {
		t.Fatal(err)
	}

	chunk := make([]byte, 1<<20)
	for written := int64(0); written < int64(count)*size; written += int64(len(chunk)) {
		if _, err := writer.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}

	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	return file.Bytes()
}

// nonZero counts the bytes written to it that are not zero.
type nonZero struct{ count int }

func (w *nonZero) Write(p []byte) (int, error) {
	for _, b := range p {
		if b != 0 {
			w.count++
		}
	}

	return len(p), nil
}

// live is the heap that a collection leaves.
func live() uint64 {
	//revive:disable-next-line:call-to-gc what the reader holds is measured from a collected heap
	runtime.GC()

	var stats runtime.MemStats

	runtime.ReadMemStats(&stats)

	return stats.HeapAlloc
}

// TestContractReaderWithWorkersHoldsWithinItsBound: "a block is as large as
// its file made it", and a reader with workers "reads in order a file whose
// blocks are too large for two" within its memory limit: a file of two
// blocks of 128 MiB, a few kilobytes long, is read with workers and a limit
// of 1 GiB without either block being held whole, and reads as what it
// holds. It runs alone: it measures the heap.
//
//nolint:paralleltest // the heap it measures is every test's
func TestContractReaderWithWorkersHoldsWithinItsBound(t *testing.T) {
	const blockSize = 128 << 20

	file := zeroBlocks(t, 2, blockSize)
	if len(file) > 1<<20 {
		t.Fatalf("the file is %d bytes", len(file))
	}

	before := live()

	reader, err := xz.NewReader(bytes.NewReader(file), xz.WithWorkers(4), xz.WithMemoryLimit(1<<30))
	if err != nil {
		t.Fatal(err)
	}

	// Past the first block: workers would hold the second by now, decoded,
	// and the first still.
	var counted nonZero

	if _, err = io.CopyN(&counted, reader, blockSize+1); err != nil {
		t.Fatal(err)
	}

	if grown := int64(live()) - int64(before); grown > blockSize/2 {
		t.Errorf("the reader holds %d MiB more than before it was made", grown>>20)
	}

	rest, err := io.Copy(&counted, reader)
	if err != nil || rest != blockSize-1 || counted.count != 0 {
		t.Errorf("%d bytes after the first block and one, %d not zero, %v", rest, counted.count, err)
	}

	if err = reader.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

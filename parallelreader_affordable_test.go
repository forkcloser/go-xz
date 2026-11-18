// Copyright 2026 Forkcloser. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package xz //nolint:testpackage // white-box: from outside, the workers a file gets are seen only by the gigabyte they hold

import (
	"bytes"
	"testing"
)

// TestAffordableWorkers: a reader "uses as many of its workers as keep all
// of that within 1 GiB": never more than it was asked for, as many as
// three blocks a worker and six more allow for the file's largest block,
// wherever in the file that block is, and fewer than two, which is read in
// order, when the largest is too large for two. It is tested from inside
// the package: from outside, the workers a file gets are seen only by what
// they hold, and at the bound that is the gigabyte.
func TestAffordableWorkers(t *testing.T) {
	t.Parallel()

	const mib = 1 << 20

	sized := func(sizes ...int64) []blockDesc {
		blocks := make([]blockDesc, len(sizes))
		for i, size := range sizes {
			blocks[i].uncompressedSize = size
		}

		return blocks
	}

	for name, row := range map[string]struct {
		blocks []blockDesc
		asked  int
		want   int
	}{
		"what xz -T writes by default, many asked": {sized(24*mib, 24*mib, 24*mib), 64, 12},
		"what xz -T writes by default, few asked":  {sized(24*mib, 24*mib, 24*mib), 4, 4},
		"small blocks":                     {sized(mib, mib), 18, 18},
		"the largest two workers can have": {sized(85*mib, 85*mib), 8, 2},
		"a megabyte more":                  {sized(86*mib, 86*mib), 8, 1},
		"a small block before a large one": {sized(mib, 86*mib), 8, 1},
		"a large block before a small one": {sized(86*mib, mib), 8, 1},
		"too large for one":                {sized(512*mib, 512*mib), 8, -1},
		"blocks of no size":                {sized(0, 0), 8, 8},
	} {
		got := affordable(row.blocks, row.asked)
		if got != row.want {
			t.Errorf("%s: %d workers, want %d", name, got, row.want)
		}

		// Whatever it answers above one is within the bound.
		if largest := max(row.blocks[0].uncompressedSize, row.blocks[len(row.blocks)-1].uncompressedSize); got > 1 &&
			int64(3*got+6)*largest > parallelMemory {
			t.Errorf("%s: %d workers hold %d MiB", name, got, int64(3*got+6)*largest/mib)
		}
	}
}

// TestConcurrentTakesTheWorkersItCanAfford: the reader a file gets has the
// workers its largest block allows, not the ones it was asked for. Three
// blocks of 24 MiB with 64 asked are read by 12: 64 would hold 198 blocks,
// 4.6 GiB. Nothing is decoded: the count is read off the reader.
func TestConcurrentTakesTheWorkersItCanAfford(t *testing.T) {
	t.Parallel()

	const mib = 1 << 20

	var file bytes.Buffer

	writer, err := NewWriter(&file, WithBlockSize(24*mib))
	if err != nil {
		t.Fatal(err)
	}

	chunk := make([]byte, mib)
	for range 3 * 24 {
		if _, err = writer.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}

	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}

	o, err := apply([]Option{WithWorkers(64)}, readerOptions, "NewReader")
	if err != nil {
		t.Fatal(err)
	}

	par, err := concurrent(bytes.NewReader(file.Bytes()), o)
	if err != nil || par == nil {
		t.Fatalf("no reader with workers: %v", err)
	}

	if par.opts.workers != 12 {
		t.Errorf("%d workers, want 12", par.opts.workers)
	}

	if err = par.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

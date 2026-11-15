// Copyright 2026 Forkcloser. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package xz_test

import (
	"bytes"
	"io"
	"testing"

	"github.com/forkcloser/xz/v2"
)

// TestParallelReaderSizeThatLies: a size larger than the bytes the
// io.ReaderAt serves makes the file unreadable from its end, so it is read
// in order, as it is without workers: whole, the stream being all there,
// whether the lie is one group of four or a megabyte. (A source that stops
// serving part of a block it had is cut short: TestContractSourceServesLess.)
func TestParallelReaderSizeThatLies(t *testing.T) {
	t.Parallel()

	data := cData(5000)
	file := refStream(refOptions{check: flagCRC32, dictCode: 20}, refBlocks(t, data, 1000))

	for _, extra := range []int64{4, 12, 4096, 1 << 20} {
		for name, opts := range map[string][]xz.Option{"workers": {xz.WithWorkers(4)}, "no workers": nil} {
			r, err := xz.NewReader(io.NewSectionReader(bytes.NewReader(file), 0, int64(len(file))+extra), opts...)
			if err != nil {
				t.Fatalf("size overstated by %d, %s: %v", extra, name, err)
			}

			got, err := io.ReadAll(r)
			_ = r.Close()

			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("size overstated by %d, %s: %d bytes, %v", extra, name, len(got), err)
			}
		}
	}
}

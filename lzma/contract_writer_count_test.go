// Copyright 2026 Forkcloser. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lzma_test

import (
	"errors"
	"io"
	"testing"

	"github.com/forkcloser/go-xz/v2/lzma"
)

// byteMiscounter is miscounter as a destination that takes single bytes as
// well, which the classic writer writes to with no buffer of its own.
type byteMiscounter struct{ miscounter }

func (m *byteMiscounter) WriteByte(c byte) error {
	_, err := m.Write([]byte{c})

	return err
}

// TestContractWriterHoldsAByteWriterToItsCount: NewWriter "writes the
// header to lzma", and a destination that takes less of it than it was
// given and reports no error has not been written to: NewWriter returns
// io.ErrShortWrite and no writer. One that reports more than it was given
// is an error too. For a destination that takes no single bytes the header
// goes "to the buffer the writer puts before it", and the same shortfall
// is io.ErrShortWrite at Close, where the buffer goes out.
func TestContractWriterHoldsAByteWriterToItsCount(t *testing.T) {
	t.Parallel()

	w, err := lzma.NewWriter(&byteMiscounter{miscounter{at: 1, delta: -1}})
	if w != nil || !errors.Is(err, io.ErrShortWrite) {
		t.Errorf("a header a byte short: writer %v, %v; want io.ErrShortWrite", w != nil, err)
	}

	w, err = lzma.NewWriter(&byteMiscounter{miscounter{at: 1, delta: 3}})
	if w != nil || err == nil {
		t.Errorf("a header counted 3 too many: writer %v, %v; want an error", w != nil, err)
	}

	// The same destination without single bytes, through the writer's own
	// buffer: the header goes out at Close, and is held to its count there.
	m := &miscounter{at: 1, delta: -1}

	plain, err := lzma.NewWriter(struct{ io.Writer }{m})
	if err != nil {
		t.Fatalf("NewWriter over a plain writer: %v", err)
	}

	if err = plain.Close(); !errors.Is(err, io.ErrShortWrite) {
		t.Errorf("a plain writer a byte short: Close: %v; want io.ErrShortWrite", err)
	}
}

// Copyright 2026 Forkcloser. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package fullwrite holds a writer to io.Writer's contract, for the
// writers of this module: each emits a format in several writes and counts
// on every one of them having gone out whole.
package fullwrite

import (
	"errors"
	"io"
)

// ErrCount is a write that reports more bytes than it was given, or fewer
// than none.
var ErrCount = errors.New("xz: the underlying writer returned an impossible count")

// Writer wraps a writer that may break io.Writer's contract: a write that
// takes less than it was given and reports no error is io.ErrShortWrite,
// and a count outside what it was given is ErrCount.
type Writer struct {
	W io.Writer
}

// Write writes p to the wrapped writer.
func (w Writer) Write(p []byte) (int, error) {
	n, err := w.W.Write(p)

	switch {
	case n < 0:
		return 0, ErrCount
	case n > len(p):
		return len(p), ErrCount
	case n < len(p) && err == nil:
		return n, io.ErrShortWrite
	}

	return n, err
}

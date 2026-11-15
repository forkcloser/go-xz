// Copyright 2026 Forkcloser. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lzma

import (
	"errors"
	"fmt"
)

// Sentinel errors that callers can test for with errors.Is.
//
// The decoders read untrusted input, and the distinction that matters to a
// caller is between a stream that is not valid LZMA or LZMA2 and a source
// that failed underneath the decoder: the first means reject the input, the
// second says nothing about it. An I/O error from the underlying reader
// matches neither sentinel and is returned as it is, with one exception:
// input that ends inside a stream, with io.EOF or an error wrapping it, is
// reported as io.ErrUnexpectedEOF. A reader keeps what it failed with:
// every later Read returns it.
//
// What a caller's own mistake earns matches neither sentinel: an option out
// of the format's range, a write after Close. Properties the format allows
// and this package does not, lc and lp summing above four, are
// ErrUnsupported in an option as in a header.
var (
	// ErrCorrupt reports that the data being decoded is not a valid LZMA or
	// LZMA2 stream: an invalid header or chunk header, a match that reaches
	// before the start of the dictionary, a range coder state no encoder
	// produces, sizes that disagree with the data. Every such error from
	// this package matches it.
	ErrCorrupt = errors.New("lzma: corrupt input")

	// ErrUnsupported reports a stream this package will not decode even
	// though it may be well formed, such as a classic LZMA header declaring
	// a stream larger than the implementation supports.
	ErrUnsupported = errors.New("lzma: unsupported feature")
)

// kindError carries a specific message while matching one of the sentinels
// above. Error returns the message alone: the sentinel is reachable through
// Unwrap, so errors.Is works without the sentinel text being prepended to
// every diagnostic.
type kindError struct {
	msg  string
	kind error
}

func (e *kindError) Error() string { return e.msg }
func (e *kindError) Unwrap() error { return e.kind }

// corruptf builds an error that describes how the input is malformed and
// matches ErrCorrupt.
func corruptf(format string, args ...any) error {
	return &kindError{msg: fmt.Sprintf(format, args...), kind: ErrCorrupt}
}

// unsupportedf builds an error that matches ErrUnsupported.
func unsupportedf(format string, args ...any) error {
	return &kindError{msg: fmt.Sprintf(format, args...), kind: ErrUnsupported}
}

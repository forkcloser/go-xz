// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lzma

// operation represents an operation on the dictionary during encoding or
// decoding. It is a value type, so that neither allocates a boxed interface
// value for each operation.
//
// An operation is either a single-byte literal (literal == true) or a
// match at the given distance and length.
type operation struct {
	// literal reports whether the operation is a single-byte literal.
	// When false, the operation is a match.
	literal bool
	// b holds the literal byte, valid when literal is true.
	b byte
	// distance supports all possible distance values, including the eos
	// marker; valid when literal is false.
	distance int64
	// n is the number of bytes represented by the operation. It is 1 for
	// a literal and the match length otherwise.
	n int
}

// litOp returns a literal operation for the given byte.
func litOp(b byte) operation {
	return operation{literal: true, b: b, n: 1}
}

// matchOp returns a match operation for the given distance and length.
func matchOp(distance int64, n int) operation {
	return operation{distance: distance, n: n}
}

// Len returns the number of bytes represented by the operation.
func (o operation) Len() int {
	return o.n
}

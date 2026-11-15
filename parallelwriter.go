// Copyright 2026 Forkcloser. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package xz

import (
	"bytes"
	"hash"
	"io"
)

// minParallelBlockSize is the smallest block a writer with workers cuts
// when no block size is asked for: what xz -T uses.
const minParallelBlockSize = 1 << 20

// parallelBlockSize is the block size of a writer with workers that was
// given none: three times the dictionary and at least 1 MiB, as xz -T. The
// blocks are then the same whatever the number of workers and of CPUs.
func parallelBlockSize(dictSize int) int64 {
	return max(3*int64(dictSize), minParallelBlockSize)
}

// encodedBlock is a block as it goes to the file: its header, compressed
// data, padding and check, with its index record; or the error that kept
// it from being encoded. in is the buffer its data came in, to use again.
type encodedBlock struct {
	in   []byte
	data []byte
	rec  record
	err  error
}

// blockEncoders encodes the blocks of a file concurrently and writes them
// in order. Every block is encoded as the sequential writer encodes it,
// from nothing, so the file is the same bytes.
//
// All of it runs on the caller's goroutine but the encoding itself: the
// underlying writer sees one Write at a time, and an error of its own
// reaches the caller from the Write or Close that met it.
type blockEncoders struct {
	opts    *options
	newHash func() hash.Hash
	// limit is how many blocks may be encoding at once. Each holds its
	// data and what it compresses to until it is written.
	limit int
	// block is the block being filled, never past opts.blockSize. It is
	// handed to an encoder when more data comes, or at Close: a file's
	// last block is cut where the data ends, and an empty file is one
	// empty block, as the sequential writer writes them.
	block []byte
	// pending is the blocks handed over and not yet written, oldest first.
	pending []chan encodedBlock
	spare   [][]byte
}

// write adds p to the file, handing a block over each time one is full and
// more data follows.
func (e *blockEncoders) write(xz io.Writer, index *[]record, p []byte) (n int, err error) {
	for len(p) > 0 {
		room := int(e.opts.blockSize) - len(e.block)
		if room == 0 {
			if err = e.handOver(xz, index); err != nil {
				return n, err
			}

			continue
		}

		k := min(room, len(p))
		e.block = e.grown(len(e.block) + k)
		e.block = append(e.block, p[:k]...)
		p = p[k:]
		n += k
	}

	return n, nil
}

// grown returns the block with room for need bytes: doubled, and never
// past the block size, so a small file costs what it holds.
func (e *blockEncoders) grown(need int) []byte {
	if need <= cap(e.block) {
		return e.block
	}

	size := min(max(2*cap(e.block), need), int(e.opts.blockSize))

	return append(make([]byte, 0, size), e.block...)
}

// handOver gives the block being filled to an encoder of its own, once the
// oldest pending block has been written when limit of them are out.
func (e *blockEncoders) handOver(xz io.Writer, index *[]record) error {
	if len(e.pending) == e.limit {
		if err := e.writeOldest(xz, index); err != nil {
			return err
		}
	}

	// Buffered: an encoder whose block is never collected, after an error
	// or an abandoned writer, ends all the same.
	done := make(chan encodedBlock, 1)
	data, check := e.block, e.newHash()

	go func() { done <- e.opts.encodeBlock(check, data) }()

	e.pending = append(e.pending, done)
	e.block = nil

	if last := len(e.spare) - 1; last >= 0 {
		e.block, e.spare = e.spare[last], e.spare[:last]
	}

	return nil
}

// writeOldest waits for the oldest pending block and writes it.
func (e *blockEncoders) writeOldest(xz io.Writer, index *[]record) error {
	block := <-e.pending[0]
	e.pending = e.pending[1:]
	e.spare = append(e.spare, block.in[:0])

	if block.err != nil {
		return block.err
	}

	if _, err := xz.Write(block.data); err != nil {
		return err
	}

	*index = append(*index, block.rec)

	return nil
}

// finish hands the last block over and writes every pending one.
func (e *blockEncoders) finish(xz io.Writer, index *[]record) error {
	if err := e.handOver(xz, index); err != nil {
		return err
	}

	for len(e.pending) > 0 {
		if err := e.writeOldest(xz, index); err != nil {
			return err
		}
	}

	return nil
}

// encodeBlock encodes data as one block, the way the sequential writer
// writes one: header, compressed data, padding, check.
func (o *options) encodeBlock(check hash.Hash, data []byte) encodedBlock {
	var out bytes.Buffer

	bw, err := o.newBlockWriter(&out, check)
	if err != nil {
		return encodedBlock{in: data, err: err}
	}

	if err = bw.writeHeader(&out); err != nil {
		return encodedBlock{in: data, err: err}
	}

	if _, err = bw.Write(data); err != nil {
		return encodedBlock{in: data, err: err}
	}

	if err = bw.Close(); err != nil {
		return encodedBlock{in: data, err: err}
	}

	return encodedBlock{in: data, data: out.Bytes(), rec: bw.record()}
}

// Copyright 2026 Forkcloser. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sysmem

import (
	"encoding/binary"
	"syscall"
)

func physical() uint64 {
	value, err := syscall.Sysctl("hw.memsize")
	if err != nil {
		return 0
	}

	// Sysctl returns the eight bytes of the number as a string, less a
	// final zero byte it takes for a terminator.
	var raw [8]byte

	copy(raw[:], value)

	return binary.LittleEndian.Uint64(raw[:])
}

func confined() uint64 { return 0 }

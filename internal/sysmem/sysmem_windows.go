// Copyright 2026 Forkcloser. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sysmem

import (
	"syscall"
	"unsafe"
)

// memoryStatusEx is MEMORYSTATUSEX.
type memoryStatusEx struct {
	length               uint32
	memoryLoad           uint32
	totalPhys            uint64
	availPhys            uint64
	totalPageFile        uint64
	availPageFile        uint64
	totalVirtual         uint64
	availVirtual         uint64
	availExtendedVirtual uint64
}

func physical() uint64 {
	var status memoryStatusEx

	status.length = uint32(unsafe.Sizeof(status))

	// #nosec G103 -- the structure is passed to the system call that fills it
	ok, _, _ := syscall.NewLazyDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx").Call(
		uintptr(unsafe.Pointer(&status)),
	)
	if ok == 0 {
		return 0
	}

	return status.totalPhys
}

func confined() uint64 { return 0 }

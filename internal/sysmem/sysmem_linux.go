// Copyright 2026 Forkcloser. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sysmem

import "syscall"

func physical() uint64 {
	var info syscall.Sysinfo_t
	if err := syscall.Sysinfo(&info); err != nil {
		return 0
	}

	//nolint:unconvert // Totalram is 32 bits wide on the 32-bit ports
	return uint64(info.Totalram) * uint64(info.Unit)
}

func confined() uint64 { return confinedBy("/proc/self/cgroup", "/sys/fs/cgroup") }

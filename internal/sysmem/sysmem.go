// Copyright 2026 Forkcloser. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package sysmem reports how much memory the process can count on.
package sysmem

import (
	"math"
	"runtime/debug"
)

// Total is the memory the process can have, in bytes: the machine's
// physical memory, or what is lower of the limit of the control group the
// process runs in, as in a container, and the program's own memory limit
// (GOMEMLIMIT, debug.SetMemoryLimit). It is 0 when nothing says.
func Total() uint64 {
	total := physical()

	lower := func(limit uint64) {
		if limit > 0 && (total == 0 || limit < total) {
			total = limit
		}
	}

	lower(confined())

	// Reading the program's limit takes a negative argument and changes
	// nothing; unset, it is the largest int64.
	if own := debug.SetMemoryLimit(-1); own > 0 && own != math.MaxInt64 {
		lower(uint64(own))
	}

	return total
}

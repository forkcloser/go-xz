// Copyright 2026 Forkcloser. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sysmem_test

import (
	"math"
	"runtime"
	"runtime/debug"
	"testing"

	"github.com/forkcloser/go-xz/v2/internal/sysmem"
)

// TestTotal: on the platforms that say, the memory the process can have is
// known, and is a machine's: at least 64 MiB, 2 GiB on darwin, and no more
// than an exbibyte. Then the program's own limit, when lower, is the
// answer, and a higher one is not. It runs alone: the limit is the
// process's.
//
//nolint:paralleltest // it sets the process's memory limit
func TestTotal(t *testing.T) {
	// Whatever limit the environment gave the test (GOMEMLIMIT) is lifted
	// while it runs: the machine is read first, then limits of the test's.
	defer debug.SetMemoryLimit(debug.SetMemoryLimit(math.MaxInt64))

	total := sysmem.Total()

	// The floor on darwin is what the smallest Mac this runs on has: the
	// eight bytes of hw.memsize read the wrong way round come out under it.
	floor := map[string]uint64{"linux": 64 << 20, "windows": 64 << 20, "darwin": 2 << 30}[runtime.GOOS]
	if floor == 0 {
		if total != 0 {
			t.Fatalf("Total = %d bytes on %s, which this package does not read", total, runtime.GOOS)
		}

		return
	}

	if total < floor || total > 1<<60 {
		t.Fatalf("Total = %d bytes on %s", total, runtime.GOOS)
	}

	const own = 48 << 20

	debug.SetMemoryLimit(own)

	if got := sysmem.Total(); got != own {
		t.Errorf("with the program's limit at %d: Total = %d", own, got)
	}

	debug.SetMemoryLimit(int64(total) * 2)

	if got := sysmem.Total(); got != total {
		t.Errorf("with the program's limit above the machine's memory: Total = %d, want %d", got, total)
	}
}

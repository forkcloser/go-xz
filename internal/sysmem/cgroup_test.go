// Copyright 2026 Forkcloser. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sysmem //nolint:testpackage // white-box: a control group's limit is the host's to set, so the reading is tested on trees laid out for it

import (
	"os"
	"path/filepath"
	"testing"
)

// laid writes a control group mount and a membership listing under a
// directory of the test's, and returns their paths.
func laid(t *testing.T, listing string, files map[string]string) (membership, root string) {
	t.Helper()

	dir := t.TempDir()
	membership = filepath.Join(dir, "cgroup")
	root = filepath.Join(dir, "mount")

	if err := os.WriteFile(membership, []byte(listing), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}

	for name, text := range files {
		file := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(file, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	return membership, root
}

// TestConfinedBy: the limit is the lowest among the process's group and
// its ancestors, in the unified hierarchy and in the memory controller's
// own; "max", zero and what is no number are no limit; a group that is not
// under the mount, as a container's own is not, is read from its parents;
// and a name cannot lead out of the mount.
func TestConfinedBy(t *testing.T) {
	t.Parallel()

	const (
		gib   = 1 << 30
		unset = "9223372036854771712\n"
	)

	for name, row := range map[string]struct {
		listing string
		files   map[string]string
		want    uint64
	}{
		"v2, a container seen from inside": {"0::/\n", map[string]string{"memory.max": "2147483648\n"}, 2 * gib},
		"v2, the lowest of three levels": {
			"0::/a/b\n",
			map[string]string{"memory.max": "max\n", "a/memory.max": "1073741824\n", "a/b/memory.max": "4294967296\n"},
			gib,
		},
		"v2, no limit anywhere":         {"0::/a\n", map[string]string{"memory.max": "max\n", "a/memory.max": "max\n"}, 0},
		"v2, zero and no number":        {"0::/a\n", map[string]string{"memory.max": "0\n", "a/memory.max": "lots\n"}, 0},
		"v2, a group that is not there": {"0::/gone/deeper\n", map[string]string{"memory.max": "1073741824\n"}, gib},
		"v2, a name that climbs":        {"0::/../../out\n", map[string]string{"memory.max": "1073741824\n"}, gib},
		"v1, a container": {
			"12:memory:/docker/abc\n3:cpu,cpuacct:/docker/abc\n",
			map[string]string{"memory/memory.limit_in_bytes": "2147483648\n"},
			2 * gib,
		},
		"v1, among other controllers": {
			"7:cpuset,memory:/job\n",
			map[string]string{"memory/memory.limit_in_bytes": unset, "memory/job/memory.limit_in_bytes": "536870912\n"},
			gib / 2,
		},
		"v1, no limit": {"12:memory:/\n", map[string]string{"memory/memory.limit_in_bytes": unset}, 9223372036854771712},
		"hybrid, the limit in v1": {
			"12:memory:/docker/abc\n0::/init.scope\n",
			map[string]string{"memory/memory.limit_in_bytes": "2147483648\n"},
			2 * gib,
		},
		"both, the lower": {
			"12:memory:/\n0::/\n",
			map[string]string{"memory/memory.limit_in_bytes": "4294967296\n", "memory.max": "1073741824\n"},
			gib,
		},
		"both, the lower in v1": {
			"12:memory:/\n0::/\n",
			map[string]string{"memory/memory.limit_in_bytes": "536870912\n", "memory.max": "1073741824\n"},
			gib / 2,
		},
		"a listing with neither": {"3:cpu:/\n", map[string]string{"memory.max": "1073741824\n"}, 0},
	} {
		membership, root := laid(t, row.listing, row.files)
		if got := confinedBy(membership, root); got != row.want {
			t.Errorf("%s: %d, want %d", name, got, row.want)
		}
	}

	if got := confinedBy(filepath.Join(t.TempDir(), "absent"), t.TempDir()); got != 0 {
		t.Errorf("no listing: %d, want 0", got)
	}
}

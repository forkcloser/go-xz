// Copyright 2026 Forkcloser. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sysmem

import (
	"bytes"
	"path"
	"path/filepath"
	"slices"
	"strconv"

	"github.com/forkcloser/go-xos"
)

// confinedBy is the lowest memory limit among the control groups the
// process is in and their ancestors, 0 when there is none: memory.max in
// the unified (v2) hierarchy mounted at root, memory.limit_in_bytes in the
// memory controller's own (v1) under it. membership is the path of the
// process's /proc/self/cgroup. A group that is not there under the mount,
// as a container's own is not, is read from the parents that are.
func confinedBy(membership, root string) uint64 {
	// #nosec G304 -- the kernel's listing of the process's own groups
	listing, err := xos.ReadFile(membership)
	if err != nil {
		return 0
	}

	var lowest uint64

	for _, hierarchy := range []struct {
		controller, mount, file string
	}{
		{"", root, "memory.max"},
		{"memory", filepath.Join(root, "memory"), "memory.limit_in_bytes"},
	} {
		name, ok := group(listing, hierarchy.controller)
		if !ok {
			continue
		}

		if limit := lowestUnder(hierarchy.mount, name, hierarchy.file); limit > 0 && (lowest == 0 || limit < lowest) {
			lowest = limit
		}
	}

	return lowest
}

// lowestUnder is the lowest limit file holds from the group name down to
// mount, the mount's own included; 0 when none holds one.
func lowestUnder(mount, name, file string) uint64 {
	var lowest uint64

	for dir := filepath.Join(mount, filepath.FromSlash(name)); ; dir = filepath.Dir(dir) {
		// #nosec G304 -- a path under the control group mount, named by the kernel's own listing
		if text, err := xos.ReadFile(filepath.Join(dir, file)); err == nil {
			if limit, limited := limitOf(text); limited && (lowest == 0 || limit < lowest) {
				lowest = limit
			}
		}

		if len(dir) <= len(mount) {
			return lowest
		}
	}
}

// group is the process's group in one hierarchy of a /proc/self/cgroup
// listing, whose lines are "id:controllers:/name": the unified hierarchy's
// line has no controller, and "" asks for it; a v1 hierarchy's names its
// controllers, comma-separated.
func group(listing []byte, controller string) (string, bool) {
	for line := range bytes.Lines(listing) {
		fields := bytes.SplitN(bytes.TrimSpace(line), []byte(":"), 3)
		if len(fields) != 3 {
			continue
		}

		controllers := bytes.Split(fields[1], []byte(","))
		if len(fields[1]) == 0 {
			controllers = nil
		}

		if controller == "" && controllers == nil ||
			controller != "" &&
				slices.ContainsFunc(controllers, func(c []byte) bool { return string(c) == controller }) {
			return path.Clean("/" + string(fields[2])), true
		}
	}

	return "", false
}

// limitOf reads a limit: a count of bytes, or "max" for none. A v1 group
// with no limit holds a number near the largest int64, which no machine's
// memory is below.
func limitOf(text []byte) (uint64, bool) {
	limit, err := strconv.ParseUint(string(bytes.TrimSpace(text)), 10, 64)

	return limit, err == nil && limit > 0
}

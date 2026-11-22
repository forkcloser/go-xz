// Copyright 2026 Forkcloser. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !linux && !darwin && !windows

package sysmem

func physical() uint64 { return 0 }

func confined() uint64 { return 0 }

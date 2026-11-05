// Process-local wall-clock fault injection for a disposable Go 1.26.0 toolchain.
// Monotonic readings remain unchanged; production builds never use this file.

//go:build !faketime && !windows && !(linux && amd64) && !plan9

package runtime

import (
	"internal/runtime/atomic"
	_ "unsafe"
)

var k6ProbeWallStepNanos int64

//go:linkname k6ProbeSetWallStepNanos
func k6ProbeSetWallStepNanos(step int64) {
	atomic.Storeint64(&k6ProbeWallStepNanos, step)
}

//go:linkname time_now time.now
func time_now() (sec int64, nsec int32, mono int64) {
	sec, nsec = walltime()
	if step := atomic.Loadint64(&k6ProbeWallStepNanos); step != 0 {
		total := sec*1e9 + int64(nsec) + step
		sec = total / 1e9
		nsec = int32(total % 1e9)
	}
	return sec, nsec, nanotime()
}

//go:build darwin

package apple

import (
	"testing"

	"github.com/ebitengine/purego"
)

func TestNowIsMonotonic(t *testing.T) {
	a := Now()
	b := Now()
	if b < a {
		t.Fatalf("mach clock went backwards: %d then %d", a, b)
	}
}

// BenchmarkRegisterFunc measures a purego reflective call, the style used for
// most framework calls in this spike.
func BenchmarkRegisterFunc(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		MachAbsoluteTime()
	}
}

// BenchmarkSyscallN measures the non-reflective path a hot loop can use.
func BenchmarkSyscallN(b *testing.B) {
	fn := Sym(System, "mach_absolute_time")
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		purego.SyscallN(fn)
	}
}

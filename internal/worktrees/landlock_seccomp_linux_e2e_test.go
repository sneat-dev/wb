//go:build linux && e2e

package worktrees

import (
	"runtime"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

// The fixture supports the Linux amd64 syscall ABI, not x32. seccomp_data's
// argument halves are little-endian here; architecture is checked before nr.
const landlockX32SyscallBit = uint32(0x40000000)

type landlockSeccompDenial struct {
	syscall  uint32
	argument int // -1 denies every invocation of this syscall.
	value    uint32
	bitmask  bool
}

func landlockDenialFilter(denial landlockSeccompDenial) []unix.SockFilter {
	filter := []unix.SockFilter{
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 4},
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: unix.AUDIT_ARCH_X86_64, Jt: 1},
		{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_KILL_PROCESS},
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 0},
		{Code: unix.BPF_JMP | unix.BPF_JSET | unix.BPF_K, K: landlockX32SyscallBit, Jf: 1},
		{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_KILL_PROCESS},
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: denial.syscall, Jf: 1},
	}
	if denial.argument >= 0 {
		filter[len(filter)-1].Jf = 5
		comparison := uint16(unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K)
		if denial.bitmask {
			comparison = unix.BPF_JMP | unix.BPF_JSET | unix.BPF_K
		}
		filter = append(filter,
			unix.SockFilter{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: uint32(16 + denial.argument*8 + 4)},
			unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: 0, Jf: 3},
			unix.SockFilter{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: uint32(16 + denial.argument*8)},
			unix.SockFilter{Code: comparison, K: denial.value, Jf: 1},
		)
	}
	return append(filter,
		unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)},
		unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ALLOW},
	)
}

// Only marker-dispatched children call this, under a retained outer thread pin.
// There is no TSYNC: the test parent and every other runtime task stay unchanged.
func installLandlockDenialFilter(denial landlockSeccompDenial) error {
	filter := landlockDenialFilter(denial)
	program := unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return err
	}
	err := unix.Prctl(unix.PR_SET_SECCOMP, unix.SECCOMP_MODE_FILTER, uintptr(unsafe.Pointer(&program)), 0, 0)
	runtime.KeepAlive(filter)
	return err
}

func TestE2ELandlockDenialFiltersValidateABIAndTargetArguments(t *testing.T) {
	t.Parallel()
	target := uint32(unix.SYS_LANDLOCK_CREATE_RULESET)
	for _, tc := range []struct {
		name          string
		denial        landlockSeccompDenial
		arch, syscall uint32
		argument      uint64
		want          uint32
	}{
		{"foreign architecture", landlockSeccompDenial{syscall: target, argument: -1}, unix.AUDIT_ARCH_I386, target, 0, unix.SECCOMP_RET_KILL_PROCESS},
		{"x32 syscall ABI", landlockSeccompDenial{syscall: target, argument: -1}, unix.AUDIT_ARCH_X86_64, target | landlockX32SyscallBit, 0, unix.SECCOMP_RET_KILL_PROCESS},
		{"target syscall", landlockSeccompDenial{syscall: target, argument: -1}, unix.AUDIT_ARCH_X86_64, target, 0, unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)},
		{"unrelated syscall", landlockSeccompDenial{syscall: target, argument: -1}, unix.AUDIT_ARCH_X86_64, uint32(unix.SYS_WRITE), 0, unix.SECCOMP_RET_ALLOW},
		{"creation flags", landlockSeccompDenial{syscall: target, argument: 2}, unix.AUDIT_ARCH_X86_64, target, 0, unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)},
		{"version query allowed", landlockSeccompDenial{syscall: target, argument: 2}, unix.AUDIT_ARCH_X86_64, target, unix.LANDLOCK_CREATE_RULESET_VERSION, unix.SECCOMP_RET_ALLOW},
		{"high argument bits", landlockSeccompDenial{syscall: target, argument: 2}, unix.AUDIT_ARCH_X86_64, target, 1 << 32, unix.SECCOMP_RET_ALLOW},
		{"O_PATH with other flags", landlockSeccompDenial{syscall: unix.SYS_OPENAT, argument: 2, value: unix.O_PATH, bitmask: true}, unix.AUDIT_ARCH_X86_64, unix.SYS_OPENAT, unix.O_PATH | unix.O_CLOEXEC, unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)},
		{"counter output open allowed", landlockSeccompDenial{syscall: unix.SYS_OPENAT, argument: 2, value: unix.O_PATH, bitmask: true}, unix.AUDIT_ARCH_X86_64, unix.SYS_OPENAT, unix.O_WRONLY | unix.O_CREAT | unix.O_TRUNC, unix.SECCOMP_RET_ALLOW},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := map[uint32]uint32{0: tc.syscall, 4: tc.arch}
			if tc.denial.argument >= 0 {
				offset := uint32(16 + tc.denial.argument*8)
				data[offset], data[offset+4] = uint32(tc.argument), uint32(tc.argument>>32)
			}
			if got := evaluateLandlockFilter(t, landlockDenialFilter(tc.denial), data); got != tc.want {
				t.Fatalf("filter action = %#x, want %#x", got, tc.want)
			}
		})
	}
}

// This small interpreter checks construction without installing a policy in
// the test suite. Native child tests separately validate kernel acceptance.
func evaluateLandlockFilter(t *testing.T, filter []unix.SockFilter, data map[uint32]uint32) uint32 {
	t.Helper()
	var accumulator uint32
	for pc := 0; pc < len(filter); pc++ {
		instruction := filter[pc]
		switch instruction.Code {
		case unix.BPF_LD | unix.BPF_W | unix.BPF_ABS:
			accumulator = data[instruction.K]
		case unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, unix.BPF_JMP | unix.BPF_JSET | unix.BPF_K:
			match := accumulator == instruction.K
			if instruction.Code == unix.BPF_JMP|unix.BPF_JSET|unix.BPF_K {
				match = accumulator&instruction.K != 0
			}
			if match {
				pc += int(instruction.Jt)
			} else {
				pc += int(instruction.Jf)
			}
		case unix.BPF_RET | unix.BPF_K:
			return instruction.K
		default:
			t.Fatalf("unexpected BPF instruction %#x", instruction.Code)
		}
	}
	t.Fatal("filter fell through without a decision")
	return 0
}

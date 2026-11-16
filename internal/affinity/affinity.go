// Package affinity pins goroutines to logical CPUs.
package affinity

import (
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/noahzmr/tyr/internal/cpuset"
)

const maxCPUs = 8192

// Allowed returns the CPUs the process may run on (cgroup cpuset, taskset).
func Allowed() (cpuset.Set, error) {
	set := unix.NewCPUSet(maxCPUs)
	if err := unix.SchedGetaffinityDynamic(0, set); err != nil {
		return nil, err
	}
	out := cpuset.Set{}
	for i := 0; i < maxCPUs; i++ {
		if set.IsSet(i) {
			out = append(out, i)
		}
	}
	return out, nil
}

// Pin locks the calling goroutine to its OS thread and binds that thread to
// cpu. The goroutine must NOT call runtime.UnlockOSThread afterwards: when a
// goroutine exits while locked, the runtime terminates the thread, so no
// thread with a modified affinity mask returns to the scheduler's pool.
func Pin(cpu int) error {
	runtime.LockOSThread()
	set := unix.NewCPUSet(maxCPUs)
	set.Set(cpu)
	if err := unix.SchedSetaffinityDynamic(0, set); err != nil {
		return fmt.Errorf("sched_setaffinity cpu%d: %w", cpu, err)
	}
	if cur, err := Current(); err == nil && cur != cpu {
		// The kernel migrates the thread on the next scheduling point;
		// yield once and check again.
		runtime.Gosched()
		if cur, err = Current(); err == nil && cur != cpu {
			return fmt.Errorf("pinned to cpu%d but running on cpu%d", cpu, cur)
		}
	}
	return nil
}

// Current returns the CPU the calling thread is running on (getcpu(2)).
func Current() (int, error) {
	var cpu, node uint32
	_, _, errno := unix.RawSyscall(unix.SYS_GETCPU, uintptr(unsafe.Pointer(&cpu)), uintptr(unsafe.Pointer(&node)), 0)
	if errno != 0 {
		return -1, errno
	}
	return int(cpu), nil
}

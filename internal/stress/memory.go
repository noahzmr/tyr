package stress

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// memSize is the test size: pct of MemAvailable, but always leaving
// max(1 GiB, 5% of RAM) free.
func memSize(cfg Config, env *Env) (size uint64, why string) {
	m := env.Baseline.Memory
	if m == nil || m.AvailableBytes == 0 {
		return 0, "memory information unavailable"
	}
	pct := cfg.MemoryPercent
	if pct <= 0 || pct > 95 {
		pct = 70
	}
	size = m.AvailableBytes / 100 * uint64(pct)
	reserve := memReserve(m.TotalBytes)
	if m.AvailableBytes <= reserve+(64<<20) {
		return 0, fmt.Sprintf("only %s available; the test keeps %s free for the system", humanBytes(m.AvailableBytes), humanBytes(reserve))
	}
	size = min(size, m.AvailableBytes-reserve)
	return size &^ (1<<20 - 1), ""
}

// memtest86Info explains how to run memtest86+, which only runs before the OS.
func memtest86Info(env *Env) string {
	var found []string
	for _, g := range []string{"/boot/memtest86+*", "/boot/memtest86*", "/boot/efi/EFI/*/memtest*", "/usr/lib/memtest86+/*"} {
		m, _ := env.Collector.FS.Glob(g)
		found = append(found, m...)
	}
	if len(found) > 0 {
		return "For a pre-OS test of all RAM (including memory Linux uses) reboot and choose memtest86+ in the boot menu (" + found[0] + "). Tyr does not change the boot configuration."
	}
	return "A pre-OS test of all RAM needs memtest86+ (package memtest86+): install it, reboot and choose it in the boot menu. The in-OS test cannot check memory the kernel and running programs use."
}

func memPlan(cfg Config, env *Env, engine string) *Plan {
	p := &Plan{}
	size, why := memSize(cfg, env)
	if size == 0 {
		p.Blocked = why
		return p
	}
	m := env.Baseline.Memory
	p.Scope = []string{fmt.Sprintf("%s of %s available memory (%d%%), %s stays free", humanBytes(size), humanBytes(m.AvailableBytes),
		max(1, int(size*100/m.AvailableBytes)), humanBytes(memReserve(m.TotalBytes)))}
	p.Impact = []string{"Takes the memory above away from other programs for " + fmtDur(cfg.Duration) + "; page cache is dropped as needed.",
		"The guard stops the test if the host starts swapping, the OOM killer runs, or available memory falls below the reserve."}
	if m.SwapTotalBytes > 0 && m.SwapUsedBytes > m.SwapTotalBytes/2 {
		p.Warnings = append(p.Warnings, "swap is already more than half used")
	}
	switch engine {
	case EngBuiltin:
		p.Info = append(p.Info, "Patterns: random data, walking ones, moving inversions and address-in-address, each written and read back by one thread per CPU. Mismatches name the address (physical address when run as root).")
	case EngMemtester:
		p.Info = append(p.Info, "memtester runs its pattern suite single-threaded; without root it cannot lock the memory and is slower.")
	}
	p.Info = append(p.Info, memtest86Info(env))
	return p
}

// --- built-in -----------------------------------------------------------------

type memBuiltin struct{}

func (memBuiltin) Plan(cfg Config, env *Env) *Plan { return memPlan(cfg, env, EngBuiltin) }

// corruptMem lets tests flip a bit after a write; nil in production.
var corruptMem func(words []uint64, pass int)

func (memBuiltin) Run(ctx context.Context, cfg Config, _ *Plan, rt Runtime) error {
	env := runEnv(ctx)
	size, why := memSize(cfg, env)
	if size == 0 {
		return fmt.Errorf("%s", why)
	}
	buf, err := unix.Mmap(-1, 0, int(size), unix.PROT_READ|unix.PROT_WRITE, unix.MAP_PRIVATE|unix.MAP_ANONYMOUS|unix.MAP_NORESERVE)
	if err != nil {
		return fmt.Errorf("mmap %s: %w", humanBytes(size), err)
	}
	var once sync.Once
	release := func() { once.Do(func() { _ = unix.Munmap(buf) }) }
	defer release()
	rt.Track(closerFunc(func() error { release(); return nil }))
	words := unsafe.Slice((*uint64)(unsafe.Pointer(&buf[0])), len(buf)/8)

	workers := 1
	if c := env.Baseline.CPU; c != nil {
		workers = max(1, min(64, len(onlineCPUs(env))))
	}
	chunk := (len(words) / workers) &^ 511 // 4 KiB aligned
	var verified atomic.Uint64
	var errCount atomic.Int64
	report := func(w []uint64, i int, pattern string, want, got uint64) {
		if errCount.Add(1) > 50 {
			return
		}
		addr := uintptr(unsafe.Pointer(&w[i]))
		detail := fmt.Sprintf("virtual %#x", addr)
		if pa, ok := physAddr(addr); ok {
			detail += fmt.Sprintf(", physical %#x", pa)
		}
		rt.VerifyError(VerifyError{Subject: fmt.Sprintf("%#x", addr), Workload: pattern, Expected: fmt.Sprintf("%#016x", want),
			Got: fmt.Sprintf("%#016x", got), Detail: detail + fmt.Sprintf(", %d bits differ", popcount(want^got))})
	}
	patterns := []string{"random", "walking ones", "moving inversions", "address in address"}
	var wg sync.WaitGroup
	var pass atomic.Int64
	for wi := 0; wi < workers; wi++ {
		lo := wi * chunk
		hi := lo + chunk
		if wi == workers-1 {
			hi = len(words)
		}
		w := words[lo:hi]
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := 0; ctx.Err() == nil; p++ {
				if wi == 0 {
					pass.Store(int64(p))
					rt.Progress(-1, fmt.Sprintf("pass %d: %s", p+1, patterns[p%len(patterns)]))
				}
				memPass(ctx, w, p, wi, patterns[p%len(patterns)], report, &verified)
			}
		}()
	}
	t := time.NewTicker(time.Second)
	defer t.Stop()
	last, lastAt := uint64(0), time.Now()
	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return nil
		case now := <-t.C:
			n := verified.Load()
			rt.Metric("verified_bytes_per_s", "bytes_per_second", float64(n-last)/now.Sub(lastAt).Seconds())
			last, lastAt = n, now
		}
	}
}

type closerFunc func() error

func (f closerFunc) Close() error { return f() }

const memStep = 1 << 17 // words per ctx check (1 MiB)

func xorshift(x uint64) uint64 {
	x ^= x << 13
	x ^= x >> 7
	x ^= x << 17
	return x
}

// memPass writes and verifies one pattern over w. Verification regenerates
// the expected value instead of storing it.
func memPass(ctx context.Context, w []uint64, pass, worker int, pattern string, report func([]uint64, int, string, uint64, uint64), verified *atomic.Uint64) {
	n := len(w)
	expect := func(i int) uint64 { return 0 }
	switch pattern {
	case "random":
		seed := uint64(pass*7919+worker*104729) | 1
		x := seed
		for i := 0; i < n; i++ {
			x = xorshift(x)
			w[i] = x
			if i%memStep == 0 && ctx.Err() != nil {
				return
			}
		}
		if corruptMem != nil {
			corruptMem(w, pass)
		}
		x = seed
		for i := 0; i < n; i++ {
			x = xorshift(x)
			if w[i] != x {
				report(w, i, pattern, x, w[i])
			}
			if i%memStep == 0 && ctx.Err() != nil {
				return
			}
		}
		verified.Add(uint64(n) * 8)
		return
	case "walking ones":
		expect = func(i int) uint64 { return 1 << uint((i+pass)%64) }
	case "address in address":
		expect = func(i int) uint64 { return uint64(uintptr(unsafe.Pointer(&w[i]))) }
	case "moving inversions":
		pat := uint64(0x5555555555555555) << uint(pass%2)
		for i := 0; i < n; i++ {
			w[i] = pat
		}
		if corruptMem != nil {
			corruptMem(w, pass)
		}
		for i := 0; i < n; i++ { // ascending: verify P, write ^P
			if w[i] != pat {
				report(w, i, pattern, pat, w[i])
			}
			w[i] = ^pat
			if i%memStep == 0 && ctx.Err() != nil {
				return
			}
		}
		for i := n - 1; i >= 0; i-- { // descending: verify ^P, write P
			if w[i] != ^pat {
				report(w, i, pattern, ^pat, w[i])
			}
			w[i] = pat
			if i%memStep == 0 && ctx.Err() != nil {
				return
			}
		}
		verified.Add(uint64(n) * 8 * 2)
		return
	}
	for i := 0; i < n; i++ {
		w[i] = expect(i)
		if i%memStep == 0 && ctx.Err() != nil {
			return
		}
	}
	if corruptMem != nil {
		corruptMem(w, pass)
	}
	for i := 0; i < n; i++ {
		if e := expect(i); w[i] != e {
			report(w, i, pattern, e, w[i])
		}
		if i%memStep == 0 && ctx.Err() != nil {
			return
		}
	}
	verified.Add(uint64(n) * 8)
}

func popcount(x uint64) int {
	n := 0
	for ; x != 0; x &= x - 1 {
		n++
	}
	return n
}

// physAddr translates a virtual address via /proc/self/pagemap (root only;
// unprivileged reads return a zero PFN).
func physAddr(v uintptr) (uint64, bool) {
	f, err := os.Open("/proc/self/pagemap")
	if err != nil {
		return 0, false
	}
	defer f.Close()
	page := uint64(os.Getpagesize())
	var b [8]byte
	if _, err := f.ReadAt(b[:], int64(uint64(v)/page*8)); err != nil {
		return 0, false
	}
	e := binary.LittleEndian.Uint64(b[:])
	pfn := e & (1<<55 - 1)
	if e&(1<<63) == 0 || pfn == 0 {
		return 0, false
	}
	return pfn*page + uint64(v)%page, true
}

func onlineCPUs(env *Env) []int {
	var out []int
	if c := env.Baseline.CPU; c != nil {
		for _, l := range c.Logical {
			if l.Online {
				out = append(out, l.ID)
			}
		}
	}
	return out
}

// --- stress-ng ----------------------------------------------------------------

type memStressNG struct{}

func (memStressNG) Plan(cfg Config, env *Env) *Plan { return memPlan(cfg, env, EngStressNG) }

func (memStressNG) Run(ctx context.Context, cfg Config, _ *Plan, rt Runtime) error {
	env := runEnv(ctx)
	size, _ := memSize(cfg, env)
	n := max(1, min(32, len(onlineCPUs(env))))
	rt.Progress(-1, fmt.Sprintf("stress-ng --vm %d × %s, all methods", n, humanBytes(size/uint64(n))))
	return stressNG(ctx, cfg, rt, "memory", "--vm", strconv.Itoa(n), "--vm-bytes", strconv.FormatUint(size/uint64(n), 10)+"b", "--vm-method", "all", "--vm-keep")
}

// --- memtester ----------------------------------------------------------------

type memMemtester struct{}

func (memMemtester) Plan(cfg Config, env *Env) *Plan { return memPlan(cfg, env, EngMemtester) }

func (memMemtester) Run(ctx context.Context, cfg Config, _ *Plan, rt Runtime) error {
	size, _ := memSize(cfg, runEnv(ctx))
	mb := size >> 20
	rt.Progress(-1, fmt.Sprintf("memtester %d MiB", mb))
	loops := 0
	err := runExternal(ctx, rt, EngMemtester, []string{strconv.FormatUint(mb, 10) + "M", "1000"}, func(_, line string) {
		l := strings.TrimSpace(line)
		switch {
		case strings.Contains(l, "FAILURE"):
			rt.VerifyError(VerifyError{Subject: "memory", Workload: "memtester", Detail: l})
		case strings.HasPrefix(l, "Loop "):
			loops++
			rt.Progress(-1, "memtester "+l)
			rt.Metric("memtester_loops", "count", float64(loops))
		}
	})
	if err != nil {
		return fmt.Errorf("memtester: %w", err)
	}
	return nil
}

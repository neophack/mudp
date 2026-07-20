//go:build windows

package dockerx

import (
	"syscall"
	"unsafe"
)

var (
	kernel32DLL              = syscall.NewLazyDLL("kernel32.dll")
	procGetSystemTimes       = kernel32DLL.NewProc("GetSystemTimes")
	procGlobalMemoryStatusEx = kernel32DLL.NewProc("GlobalMemoryStatusEx")
)

type windowsFiletime struct {
	LowDateTime  uint32
	HighDateTime uint32
}

type windowsMemoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

// readHostMetricsWindows returns host CPU and memory usage on Windows.
func readHostMetricsWindows() HostMetrics {
	m := HostMetrics{}
	m.CPUPercent = readCPUPercentWindows()

	var ms windowsMemoryStatusEx
	ms.Length = uint32(unsafe.Sizeof(ms))
	if ok := globalMemoryStatusEx(&ms); ok {
		totalMB := float64(ms.TotalPhys) / 1024.0 / 1024.0
		availMB := float64(ms.AvailPhys) / 1024.0 / 1024.0
		usedMB := totalMB - availMB
		if usedMB < 0 {
			usedMB = 0
		}
		m.MemTotalMB = round2(totalMB)
		m.MemUsedMB = round2(usedMB)
		if totalMB > 0 {
			m.MemPercent = round2(usedMB / totalMB * 100)
		}
	}
	return m
}

// readCPUPercentWindows computes CPU usage from GetSystemTimes deltas.
func readCPUPercentWindows() float64 {
	var idle, kernel, user windowsFiletime
	if ok := getSystemTimes(&idle, &kernel, &user); !ok {
		return 0
	}
	idleNow := filetimeToUint64(idle)
	kernelNow := filetimeToUint64(kernel)
	userNow := filetimeToUint64(user)
	totalNow := kernelNow + userNow
	busyNow := totalNow - idleNow

	hostCache.mu.Lock()
	prevIdle := hostCache.prevIdle
	prevBusy := hostCache.prevBusy
	hostCache.prevIdle = idleNow
	hostCache.prevBusy = busyNow
	hostCache.mu.Unlock()
	if prevIdle == 0 && prevBusy == 0 {
		return 0
	}

	totalDelta := (idleNow - prevIdle) + (busyNow - prevBusy)
	if totalDelta == 0 {
		return 0
	}
	busyDelta := busyNow - prevBusy
	pct := float64(busyDelta) / float64(totalDelta) * 100
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return round2(pct)
}

func getSystemTimes(idle, kernel, user *windowsFiletime) bool {
	r1, _, _ := procGetSystemTimes.Call(
		uintptr(unsafe.Pointer(idle)),
		uintptr(unsafe.Pointer(kernel)),
		uintptr(unsafe.Pointer(user)),
	)
	return r1 != 0
}

func globalMemoryStatusEx(ms *windowsMemoryStatusEx) bool {
	r1, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(ms)))
	return r1 != 0
}

func filetimeToUint64(ft windowsFiletime) uint64 {
	return uint64(ft.HighDateTime)<<32 | uint64(ft.LowDateTime)
}

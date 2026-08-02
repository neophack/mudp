//go:build !windows

package server

import "syscall"

func diskFree(path string) (int64, error) {
	_, free, err := diskUsage(path)
	if err != nil {
		return -1, err
	}
	return int64(free), nil
}

// diskUsage returns the total and available bytes of the filesystem holding
// path.
func diskUsage(path string) (total, free uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	return st.Blocks * uint64(st.Bsize), st.Bavail * uint64(st.Bsize), nil
}

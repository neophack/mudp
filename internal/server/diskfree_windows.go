package server

import "golang.org/x/sys/windows"

func diskFree(path string) (int64, error) {
	_, free, err := diskUsage(path)
	if err != nil {
		return -1, err
	}
	return int64(free), nil
}

// diskUsage returns the total and available bytes of the volume holding path.
func diskUsage(path string) (total, free uint64, err error) {
	var freeBytes, totalBytes uint64
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, err
	}
	if err := windows.GetDiskFreeSpaceEx(pathPtr, &freeBytes, &totalBytes, nil); err != nil {
		return 0, 0, err
	}
	return totalBytes, freeBytes, nil
}

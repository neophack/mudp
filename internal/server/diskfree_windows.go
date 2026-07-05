package server

import "golang.org/x/sys/windows"

func diskFree(path string) (int64, error) {
	var freeBytes uint64
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return -1, err
	}
	err = windows.GetDiskFreeSpaceEx(pathPtr, &freeBytes, nil, nil)
	if err != nil {
		return -1, err
	}
	return int64(freeBytes), nil
}

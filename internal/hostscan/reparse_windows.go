//go:build windows

package hostscan

import (
	"os"

	"golang.org/x/sys/windows"
)

func isReparsePoint(path string, info os.FileInfo) bool {
	if info != nil && info.Mode()&os.ModeSymlink != 0 {
		return true
	}
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return true
	}
	attributes, err := windows.GetFileAttributes(pointer)
	if err != nil {
		return false
	}
	return attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
}

//go:build !windows

package hostscan

import "os"

func isReparsePoint(_ string, info os.FileInfo) bool {
	return info != nil && info.Mode()&os.ModeSymlink != 0
}

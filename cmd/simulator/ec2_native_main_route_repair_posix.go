//go:build (linux || darwin) && !sqlite3_flock && !sqlite3_dotlk

package main

import (
	"os"
	"syscall"
)

const ec2NativeMainRepairSupported = true

func isOwnedMainRepairFile(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid()) && (info.IsDir() || stat.Nlink == 1)
}

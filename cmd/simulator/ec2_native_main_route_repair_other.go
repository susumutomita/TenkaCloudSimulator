//go:build (!linux && !darwin) || sqlite3_flock || sqlite3_dotlk

package main

import "os"

const ec2NativeMainRepairSupported = false

func isOwnedMainRepairFile(_ os.FileInfo) bool { return false }

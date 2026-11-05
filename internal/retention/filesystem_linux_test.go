package retention

import (
	"fmt"
	"syscall"
)

// testFilesystem names the filesystem holding directory, so real-disk
// measurements state where they ran (AGENTS.md §17.2).
func testFilesystem(directory string) string {
	var stats syscall.Statfs_t
	if err := syscall.Statfs(directory, &stats); err != nil {
		return "unknown"
	}
	switch stats.Type {
	case 0x01021994:
		return "tmpfs (no-fsync comparison only)"
	case 0x9123683e:
		return "btrfs"
	case 0xef53:
		return "ext4"
	case 0x58465342:
		return "xfs"
	case 0x794c7630:
		return "overlayfs"
	case 0x2fc12fc1:
		return "zfs"
	default:
		return fmt.Sprintf("magic %#x", stats.Type)
	}
}

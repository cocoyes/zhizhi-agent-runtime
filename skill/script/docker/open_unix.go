//go:build unix

package docker

import (
	"os"
	"syscall"
)

const safeFileOpening = true

func openDescriptor(root *os.Root, path string, directory bool) (*os.File, error) {
	flags := os.O_RDONLY | syscall.O_NONBLOCK | syscall.O_NOFOLLOW
	if directory {
		flags |= syscall.O_DIRECTORY
	}
	return root.OpenFile(path, flags, 0)
}

//go:build !unix

package docker

import "os"

const safeFileOpening = false

func openDescriptor(root *os.Root, path string, directory bool) (*os.File, error) {
	return root.Open(path)
}

package docker

import (
	"github.com/cocoyes/zhizhi-agent-runtime/skill/script"
	"os"
)

// Inspect the descriptor actually opened, not just earlier path metadata. Unix
// uses nonblocking/no-follow flags so a FIFO replacement cannot stall cleanup.
func openConfined(root *os.Root, path string, directory bool) (*os.File, error) {
	file, err := openDescriptor(root, path, directory)
	if err != nil {
		return nil, script.Failure(script.CodePermission, err)
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, script.Failure(script.CodePermission, err)
	}
	if (directory && !info.IsDir()) || (!directory && (!info.Mode().IsRegular() || multipleLinks(info))) {
		file.Close()
		return nil, script.Failure(script.CodePermission, script.ErrPermission)
	}
	return file, nil
}

//go:build unix

package docker

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestReplacedDirectoryAndFileCannotBlockOnFIFO(t *testing.T) {
	for _, directory := range []bool{true, false} {
		root := t.TempDir()
		target := filepath.Join(root, "changing")
		if directory {
			if err := os.Mkdir(target, 0700); err != nil {
				t.Fatal(err)
			}
		} else {
			if err := os.WriteFile(target, []byte("safe"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		confined, err := os.OpenRoot(root)
		if err != nil {
			t.Fatal(err)
		}
		defer confined.Close()
		if _, err := confined.Lstat("changing"); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(target); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Mkfifo(target, 0600); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() {
			file, err := openConfined(confined, "changing", directory)
			if file != nil {
				file.Close()
			}
			done <- err
		}()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("FIFO accepted after type replacement")
			}
		case <-time.After(time.Second):
			t.Fatal("FIFO open blocked cancellation and cleanup")
		}
	}
}

//go:build !windows

package upgrade

import (
	"fmt"
	"os"
)

func replaceExecutable(path string, binary []byte) error {
	tempPath, err := writeReplacement(path, binary)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tempPath) }()
	if err = os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

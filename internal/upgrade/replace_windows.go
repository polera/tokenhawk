//go:build windows

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
	if err = os.Rename(tempPath, path); err == nil {
		return nil
	}

	// Windows may not replace a running executable directly, but permits it to
	// be renamed. Keep the old image beside the new one until it can be removed.
	backup := path + ".old"
	_ = os.Remove(backup)
	if err = os.Rename(path, backup); err != nil {
		return fmt.Errorf("move current executable aside: %w", err)
	}
	if err = os.Rename(tempPath, path); err != nil {
		_ = os.Rename(backup, path)
		return fmt.Errorf("replace %s: %w", path, err)
	}
	_ = os.Remove(backup)
	return nil
}

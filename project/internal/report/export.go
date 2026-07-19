package report

import (
	"fmt"
	"os"
	"path/filepath"
)

// Export 写出可替换副本，MySQL Report 仍是真相源。
// 临时文件落盘并 Sync 后再 Rename，读者不会看到只写了一半的 Markdown。
func Export(dir string, id uint64, markdown string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".report-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	ok := false
	defer func() {
		_ = tmp.Close()
		if !ok {
			_ = os.Remove(name)
		}
	}()
	if _, err = tmp.WriteString(markdown); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Rename(name, filepath.Join(dir, fmt.Sprintf("%d.md", id))); err != nil {
		return err
	}
	ok = true
	return nil
}

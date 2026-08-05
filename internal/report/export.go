package report

import (
	"fmt"
	"os"
	"path/filepath"
)

// Export 写出可替换副本，MySQL Report 仍是真相源。
// 临时文件落盘并 Sync 后再 Rename，读者不会看到只写了一半的 Markdown。
func Export(dir string, id uint64, markdown string) error {
	// 目录权限 0750：属主可读写执行、同组只读；目录已存在时 MkdirAll 幂等返回。
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	// 临时文件必须与目标同目录：os.Rename 跨文件系统会失败，
	// 同目录内的重命名才是原子替换。
	tmp, err := os.CreateTemp(dir, ".report-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	// ok 标记成功与失败：失败路径在 defer 里删除临时文件，成功路径保留产物，
	// 避免目录里堆积半成品 .tmp 垃圾。
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
	// 同目录 Rename 原子替换目标文件：已存在的旧报告被覆盖，读者不会看到中间态。
	if err = os.Rename(name, filepath.Join(dir, fmt.Sprintf("%d.md", id))); err != nil {
		return err
	}
	ok = true
	return nil
}

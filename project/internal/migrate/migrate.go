package migrate

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
)

//go:embed sql/*.sql
var files embed.FS

func Run(db *gorm.DB) error {
	// 已执行迁移记录内容校验和：迁移文件一旦发布就不可原地修改，只能追加新版本，
	// 否则不同环境可能在同一版本号下得到不同表结构。
	if err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version VARCHAR(255) PRIMARY KEY,
		checksum CHAR(64) NOT NULL,
		applied_at DATETIME(6) NOT NULL
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`).Error; err != nil {
		return err
	}
	entries, err := fs.Glob(files, "sql/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(entries)
	for _, name := range entries {
		body, err := files.ReadFile(name)
		if err != nil {
			return err
		}
		version := strings.TrimSuffix(strings.TrimPrefix(name, "sql/"), ".sql")
		sum := sha256.Sum256(body)
		checksum := hex.EncodeToString(sum[:])
		var existing string
		result := db.Raw("SELECT checksum FROM schema_migrations WHERE version = ?", version).Scan(&existing)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected > 0 {
			if existing != checksum {
				return fmt.Errorf("migration %s checksum changed", version)
			}
			continue
		}
		if err := db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Exec(string(body)).Error; err != nil {
				return err
			}
			return tx.Exec("INSERT INTO schema_migrations(version, checksum, applied_at) VALUES (?, ?, ?)", version, checksum, time.Now().UTC()).Error
		}); err != nil {
			return fmt.Errorf("apply migration %s: %w", version, err)
		}
	}
	return nil
}

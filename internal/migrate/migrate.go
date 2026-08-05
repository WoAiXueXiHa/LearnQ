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

// Run 幂等执行全部迁移：按文件名排序依次应用，已应用且校验和一致的版本跳过。
// 每条迁移的 SQL 与版本登记包在同一事务中：失败时版本不登记，该版本仍视为未应用；
// 但 MySQL 的 DDL 会隐式提交，多语句迁移中途失败时已执行的语句无法回滚。
func Run(db *gorm.DB) error {
	// 已执行迁移记录内容校验和：迁移文件一旦发布就不可原地修改，只能追加新版本，
	// 否则不同环境可能在同一版本号下得到不同表结构。
	// 记录表与业务表同库、幂等创建：首次运行自动初始化，重复运行无副作用。
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
	// fs.Glob 按字典序返回结果，但排序属于实现细节而非文档契约；按文件名（零填充序号，如 005_feature.sql）显式排序，
	// 保证迁移严格按版本顺序执行。
	sort.Strings(entries)
	for _, name := range entries {
		body, err := files.ReadFile(name)
		if err != nil {
			return err
		}
		// 版本号取自文件名（005_feature.sql → 005_feature），与文件内容无关。
		version := strings.TrimSuffix(strings.TrimPrefix(name, "sql/"), ".sql")
		sum := sha256.Sum256(body)
		checksum := hex.EncodeToString(sum[:])
		var existing string
		result := db.Raw("SELECT checksum FROM schema_migrations WHERE version = ?", version).Scan(&existing)
		if result.Error != nil {
			return result.Error
		}
		// 已登记过该版本：校验和一致则跳过（幂等重跑），不一致说明发布后文件被修改，直接拒绝执行。
		if result.RowsAffected > 0 {
			if existing != checksum {
				return fmt.Errorf("migration %s checksum changed", version)
			}
			continue
		}
		// 迁移 SQL 与版本登记同事务：失败时版本登记一并回滚，该版本仍视为未应用。
		if err := db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Exec(string(body)).Error; err != nil {
				return err
			}
			// applied_at 取 UTC，避免本地时区差异干扰多环境排障。
			return tx.Exec("INSERT INTO schema_migrations(version, checksum, applied_at) VALUES (?, ?, ?)", version, checksum, time.Now().UTC()).Error
		}); err != nil {
			// 包装错误带上版本号，失败时能直接定位到具体迁移文件。
			return fmt.Errorf("apply migration %s: %w", version, err)
		}
	}
	return nil
}

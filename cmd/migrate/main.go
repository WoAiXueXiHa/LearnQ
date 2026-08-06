package main

import (
	"log"

	"github.com/WoAiXueXiHa/LearnQ/internal/bootstrap"
	"github.com/WoAiXueXiHa/LearnQ/internal/config"
	"github.com/WoAiXueXiHa/LearnQ/internal/migrate"
)

// main 是迁移入口：连接 MySQL 后执行全部待应用的 SQL 迁移，任一失败即 log.Fatal
// 终止进程。作为部署编排中的独立步骤运行，先于 API/Worker 完成表结构升级。
func main() {
	// 迁移使用独立入口，部署流程可在 API/Worker 启动前显式完成表结构升级，
	// 避免多个业务进程并发启动时争抢 DDL。
	// 直接 Load 而不 Validate：迁移只需要 DSN 即可运行，模型配置等校验项与本入口无关。
	db, err := bootstrap.MySQL(config.Load())
	if err != nil {
		log.Fatal(err)
	}
	// 失败即终止：每个迁移与其版本登记在同一事务中，失败时版本登记一并回滚，该版本仍视为未应用。
	// 注意 MySQL 的 DDL 会隐式提交：多语句迁移中途失败时，此前已执行的语句不会回滚，重跑前需自行清理。
	if err := migrate.Run(db); err != nil {
		log.Fatal(err)
	}
}

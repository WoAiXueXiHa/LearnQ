package main

import (
	"log"

	"github.com/WoAiXueXiHa/LeranQ/project/internal/bootstrap"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/config"
	"github.com/WoAiXueXiHa/LeranQ/project/internal/migrate"
)

func main() {
	// 迁移使用独立入口，部署流程可在 API/Worker 启动前显式完成表结构升级，
	// 避免多个业务进程并发启动时争抢 DDL。
	db, err := bootstrap.MySQL(config.Load())
	if err != nil {
		log.Fatal(err)
	}
	if err := migrate.Run(db); err != nil {
		log.Fatal(err)
	}
}

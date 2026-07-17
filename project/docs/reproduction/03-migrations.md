# 03 SQL Migration

运行 `GOWORK=off go run ./cmd/migrate`。migration 依次执行嵌入 SQL，并把版本与 SHA-256 checksum 写入 `schema_migrations`。

重复执行不会重建表；若已执行版本的 SQL 内容变化，程序拒绝继续，避免环境静默漂移。Compose 的 migration 服务必须成功退出后 API/worker 才启动。

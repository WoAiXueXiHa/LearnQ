# 01 环境准备

安装 Go 1.25+、Docker、Compose、curl、jq 与 make。执行 `docker info` 确认 daemon 可用，再运行 `GOWORK=off go version`。复制 `.env.example` 仅用于本地覆盖；默认 Fake 模式不需要外部 AI 密钥。

首次启动运行 `docker compose up --build -d`，随后用 `docker compose ps -a` 确认 MySQL、Redis、Qdrant healthy，migration 为 exited(0)，API 与 worker 为 running。

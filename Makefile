.PHONY: fmt fmt-check vet test test-tools race test-integration up down reset-data acceptance acceptance-practice manual-test eval-rag eval-redis-qdrant reindex

# 按 git 索引枚举 Go 文件：包含未跟踪的新文件，排除 data/reports、tmp 等被忽略的本地草稿，
# 否则这些不入库的临时脚本会让 fmt-check 永远失败。
GO_FILES := $(shell git ls-files --cached --others --exclude-standard '*.go')

fmt:
	gofmt -w $(GO_FILES)
fmt-check:
	test -z "$$(gofmt -l $(GO_FILES))"

# 只检查真正入库的代码目录：data/reports 等被忽略的目录里可能留着历史探查脚本，
# 它们既不属于交付物，也不该让 vet/test 失败。新增顶层代码目录时同步加进这里。
GO_PACKAGES := ./cmd/... ./internal/...

vet:
	GOWORK=off go vet $(GO_PACKAGES)
test:
	GOWORK=off go test $(GO_PACKAGES)
test-tools:
	python3 -m unittest discover -s scripts -p 'test_*.py' -v
race:
	GOWORK=off go test -race $(GO_PACKAGES)

test-integration:
	@test -n "$(LEARNQ_TEST_MYSQL_DSN)" || { echo 'LEARNQ_TEST_MYSQL_DSN must target disposable MySQL' >&2; exit 1; }
	@test -n "$(LEARNQ_TEST_REDIS_ADDR)" || { echo 'LEARNQ_TEST_REDIS_ADDR must target disposable Redis' >&2; exit 1; }
	GOWORK=off go test -p 1 -count=1 -timeout 120s -tags=integration $(GO_PACKAGES)
up:
	docker compose up --build -d
down:
	docker compose down
reset-data:
	docker compose down -v
acceptance:
	./scripts/acceptance.sh
acceptance-practice:
	bash ./scripts/acceptance-practice.sh
manual-test:
	./scripts/manual-test.sh
eval-redis-qdrant:
	python3 scripts/eval-redis-qdrant.py $(EVAL_REDIS_ARGS)

eval-rag:
	./scripts/eval-rag.sh
reindex:
	GOWORK=off go run ./cmd/reindex --all

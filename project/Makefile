.PHONY: fmt fmt-check vet test test-integration up down acceptance eval-rag

fmt:
	gofmt -w $$(find . -name '*.go' -type f)
fmt-check:
	test -z "$$(gofmt -l $$(find . -name '*.go' -type f))"
vet:
	GOWORK=off go vet ./...
test:
	GOWORK=off go test ./...
test-integration:
	GOWORK=off go test -p 1 -tags=integration ./...
up:
	docker compose up --build -d
down:
	docker compose down -v
acceptance:
	./scripts/acceptance.sh
eval-rag:
	curl -fsS -X POST http://127.0.0.1:8080/api/v1/evaluations/rag -H 'Content-Type: application/json' -d '{}'

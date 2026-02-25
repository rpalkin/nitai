.PHONY: proto up down logs smoke e2e unit

proto:
	buf lint
	buf generate
	cd gen/go && go mod tidy

up:
	docker compose up -d --build

down:
	docker compose down --remove-orphans

logs:
	docker compose logs -f

smoke:
	./tests/smoke.sh

e2e:
	./tests/e2e.sh

unit:
	cd go-services && go test ./...
	cd api-server && go test ./...

.PHONY: check up down smoke
check:
	go test -race -count=1 ./...
	go vet ./...
	cd frontend && npm ci && npm run build && npm run lint && npm test
up:
	docker compose up -d --build --wait
down:
	docker compose down
smoke:
	python3 scripts/smoke.py

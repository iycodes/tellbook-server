APP_NAME := booking-api

.PHONY: run test db-up db-down db-status seed-demo

run:
	go run ./cmd/api

test:
	go test ./...

db-up:
	dbmate --migrations-dir db/migrations up

db-down:
	dbmate --migrations-dir db/migrations down

db-status:
	dbmate --migrations-dir db/migrations status

seed-demo:
	go run ./cmd/seed

# Konfigurasi dibaca dari ../.env. Salin ../.env.example lebih dahulu.
ifneq (,$(wildcard ../.env))
include ../.env
export
endif

DSN      ?= $(DATABASE_URL)
TEST_DSN ?= $(subst /iceman?,/iceman_test?,$(DATABASE_URL))
COMPOSE  := docker compose --env-file ../.env -f ../deploy/docker-compose.yml

.PHONY: help up down logs run test test-short race lint tidy fresh seed

help:
	@echo "up        jalankan basis data"
	@echo "down      hentikan basis data"
	@echo "run       jalankan server API (migrasi otomatis)"
	@echo "test      seluruh uji, termasuk konkurensi"
	@echo "race      uji dengan deteksi kondisi balapan"
	@echo "fresh     hapus basis data uji lalu uji ulang dari nol"
	@echo "seed      buat pengguna pengembangan, contoh:"
	@echo "          make seed ROLE=ADMIN_OPS EMAIL=ops@iceman.test PASSWORD=rahasia"

up:
	$(COMPOSE) up -d
	@until docker exec iceman-db pg_isready -U $(POSTGRES_USER) -d $(POSTGRES_DB) >/dev/null 2>&1; do sleep 1; done
	@docker exec iceman-db psql -U $(POSTGRES_USER) -d $(POSTGRES_DB) -tc \
		"SELECT 1 FROM pg_database WHERE datname='iceman_test'" | grep -q 1 || \
		docker exec iceman-db psql -U $(POSTGRES_USER) -d $(POSTGRES_DB) -c "CREATE DATABASE iceman_test"
	@echo "basis data siap pada port $(POSTGRES_PORT)"

down:
	$(COMPOSE) down

logs:
	$(COMPOSE) logs -f db

run:
	DATABASE_URL="$(DSN)" go run ./cmd/api

test:
	ICEMAN_TEST_DSN="$(TEST_DSN)" go test ./... -count=1

test-short:
	ICEMAN_TEST_DSN="$(TEST_DSN)" go test ./... -short -count=1

race:
	ICEMAN_TEST_DSN="$(TEST_DSN)" go test ./... -race -count=1

seed:
	go run ./cmd/seed -role "$(ROLE)" -email "$(EMAIL)" -password "$(PASSWORD)"

lint:
	go vet ./...

tidy:
	go mod tidy

fresh:
	@docker exec iceman-db psql -U $(POSTGRES_USER) -d $(POSTGRES_DB) -c "DROP DATABASE IF EXISTS iceman_test" >/dev/null
	@docker exec iceman-db psql -U $(POSTGRES_USER) -d $(POSTGRES_DB) -c "CREATE DATABASE iceman_test" >/dev/null
	@$(MAKE) test

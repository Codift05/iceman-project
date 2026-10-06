# Konfigurasi dibaca dari ../.env. Salin ../.env.example lebih dahulu.
ifneq (,$(wildcard ../.env))
include ../.env
export
endif

DSN      ?= $(DATABASE_URL)
TEST_DSN ?= $(subst /iceman?,/iceman_test?,$(DATABASE_URL))
COMPOSE  := docker compose --env-file ../.env -f ../deploy/docker-compose.yml

.PHONY: help up down logs run worker test test-short race lint tidy fresh seed db-ready

help:
	@echo "up        jalankan basis data"
	@echo "down      hentikan basis data"
	@echo "run       jalankan server API (migrasi otomatis)"
	@echo "worker    jalankan pekerja latar (pembuat slot harian)"
	@echo "test      seluruh uji, termasuk konkurensi"
	@echo "race      uji dengan deteksi kondisi balapan"
	@echo "          test dan race menolak jalan bila basis data uji mati,"
	@echo "          karena uji yang melewati dirinya sendiri tetap tampak hijau"
	@echo "fresh     hapus basis data uji lalu uji ulang dari nol"
	@echo "seed      buat pengguna pengembangan, contoh:"
	@echo "          make seed ROLE=ADMIN_OPS EMAIL=ops@iceman.test PASSWORD=rahasia"
	@echo "          peran DRIVER wajib menyertakan DEPOT, misalnya DEPOT=MDO-01"

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

worker:
	DATABASE_URL="$(DSN)" go run ./cmd/worker

# -p 1 menjalankan paket uji satu per satu, tidak serentak.
#
# Seluruh paket memakai satu basis data uji yang sama, dan beberapa uji perlu
# memulai dari keadaan yang bersih secara menyeluruh: penentuan depo terdekat
# memilih di antara semua depo aktif, sehingga ujinya menonaktifkan seluruh
# depo lebih dahulu. Dijalankan serentak, penonaktifan itu ikut mematikan depo
# milik paket lain yang sedang berjalan, dan ujinya gagal sesekali tanpa pola.
#
# Pilihan lain adalah satu basis data per paket, namun untuk ukuran uji ini
# biayanya tidak sepadan: seluruh uji selesai dalam beberapa detik.
# db-ready menolak menjalankan uji bila basis data uji tidak dapat dihubungi.
#
# Uji pada repositori ini melewati dirinya sendiri ketika basis data tidak
# tersedia, supaya pembaca tanpa Docker tetap dapat menjalankan "go test".
# Akibatnya "make test" dapat tampak hijau padahal tidak ada satu pun uji yang
# benar benar berjalan, dan itu pernah terjadi: container basis data mati di
# tengah pekerjaan, dan seluruh paket tetap melaporkan ok.
#
# Penjaga ini membuat hijau palsu itu tidak mungkin terjadi lewat make.
db-ready:
	@docker exec iceman-db pg_isready -U $(POSTGRES_USER) -d $(POSTGRES_DB) >/dev/null 2>&1 || { \
		echo "basis data uji tidak dapat dihubungi. Jalankan 'make up' lebih dahulu."; \
		exit 1; }

test: db-ready
	ICEMAN_TEST_DSN="$(TEST_DSN)" go test ./... -count=1 -p 1

test-short: db-ready
	ICEMAN_TEST_DSN="$(TEST_DSN)" go test ./... -short -count=1 -p 1

race: db-ready
	ICEMAN_TEST_DSN="$(TEST_DSN)" go test ./... -race -count=1 -p 1

seed:
	go run ./cmd/seed -role "$(ROLE)" -email "$(EMAIL)" -password "$(PASSWORD)" -depot "$(DEPOT)"

lint:
	go vet ./...

tidy:
	go mod tidy

fresh:
	@docker exec iceman-db psql -U $(POSTGRES_USER) -d $(POSTGRES_DB) -c "DROP DATABASE IF EXISTS iceman_test" >/dev/null
	@docker exec iceman-db psql -U $(POSTGRES_USER) -d $(POSTGRES_DB) -c "CREATE DATABASE iceman_test" >/dev/null
	@$(MAKE) test

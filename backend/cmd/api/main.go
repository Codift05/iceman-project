// Command api menjalankan server HTTP Iceman Apps.
//
// Migrasi dijalankan lebih dahulu, sebelum server menerima lalu lintas,
// sesuai Deployment Guide Bab 4.
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"

	"github.com/iceman/backend/internal/httpx"
	"github.com/iceman/backend/internal/identity"
	"github.com/iceman/backend/internal/store"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		log.Error("DATABASE_URL belum diisi. Salin .env.example menjadi .env lebih dahulu.")
		os.Exit(2)
	}
	addr := env("HTTP_ADDR", ":8088")

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := store.Migrate(ctx, dsn); err != nil {
		log.Error("migrasi gagal", "error", err)
		os.Exit(1)
	}
	log.Info("migrasi diterapkan")

	pool, err := store.Connect(ctx, dsn)
	if err != nil {
		log.Error("koneksi basis data gagal", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	signer := identity.NewSigner(signingKey(log))
	ident := identity.NewService(pool, signer)
	identHandler := identity.NewHandler(ident)

	// Penyambung antara lapisan HTTP dan lapisan identitas. Diletakkan di sini
	// agar kedua paket tidak saling mengimpor.
	parseToken := func(raw string) (*httpx.Principal, error) {
		cl, err := signer.ParseAccess(raw)
		if err != nil {
			return nil, err
		}
		return &httpx.Principal{UserID: cl.UserID, Role: cl.Role, DepotID: cl.DepotID}, nil
	}
	checkPermission := func(c echo.Context, role, permission string) (bool, error) {
		return ident.CanCode(c.Request().Context(), role, permission)
	}

	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.Use(httpx.RequestID())
	e.Use(middleware.Recover())

	e.GET("/health", func(c echo.Context) error {
		cctx, cancel := context.WithTimeout(c.Request().Context(), 2*time.Second)
		defer cancel()
		if err := pool.Ping(cctx); err != nil {
			return httpx.Fail(c, "INTERNAL")
		}
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	v1 := e.Group("/v1")
	v1.POST("/auth/login", identHandler.Login)
	v1.POST("/auth/refresh", identHandler.Refresh)
	v1.POST("/auth/logout", identHandler.Logout)
	v1.POST("/auth/mfa/verify", identHandler.MFAVerify)
	v1.POST("/auth/mfa/enroll", identHandler.MFAEnroll)
	v1.POST("/auth/mfa/confirm", identHandler.MFAConfirm)

	secured := v1.Group("", httpx.RequireAuth(parseToken))
	secured.GET("/me", identHandler.Me)

	// Contoh endpoint berpagar izin. Peran tanpa depot.view ditolak 403.
	secured.GET("/depots", func(c echo.Context) error {
		rows, err := pool.Query(c.Request().Context(),
			`SELECT id, code, name, latitude, longitude, service_radius_km, is_active
			 FROM depots WHERE is_active ORDER BY code`)
		if err != nil {
			return httpx.Fail(c, "INTERNAL")
		}
		defer rows.Close()

		out := []map[string]any{}
		for rows.Next() {
			var (
				id, code, name string
				lat, lng, rad  float64
				active         bool
			)
			if err := rows.Scan(&id, &code, &name, &lat, &lng, &rad, &active); err != nil {
				return httpx.Fail(c, "INTERNAL")
			}
			out = append(out, map[string]any{
				"id": id, "code": code, "name": name,
				"latitude": lat, "longitude": lng,
				"service_radius_km": rad, "is_active": active,
			})
		}
		return c.JSON(http.StatusOK, map[string]any{"depots": out})
	}, httpx.RequirePermission(checkPermission, "depot.view"))

	go func() {
		log.Info("server berjalan", "addr", addr)
		if err := e.Start(addr); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server berhenti", "error", err)
			stop()
		}
	}()

	<-ctx.Done()
	log.Info("mematikan server")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := e.Shutdown(shutdownCtx); err != nil {
		log.Error("gagal mematikan dengan rapi", "error", err)
	}
}

// signingKey membaca kunci penandatangan token dari lingkungan.
//
// Bila tidak diisi, kunci acak dibangkitkan dan server tetap jalan, namun
// seluruh token menjadi tidak berlaku setiap server dinyalakan ulang. Itu
// disengaja: lebih baik merepotkan di pengembangan daripada ada kunci tetap
// yang diketahui umum ikut terbawa ke produksi.
func signingKey(log *slog.Logger) []byte {
	if v := os.Getenv("JWT_SIGNING_KEY"); v != "" {
		return []byte(v)
	}
	buf := make([]byte, 48)
	if _, err := rand.Read(buf); err != nil {
		log.Error("membangkitkan kunci sementara gagal", "error", err)
		os.Exit(1)
	}
	log.Warn("JWT_SIGNING_KEY tidak diisi, memakai kunci acak sementara. " +
		"Token akan batal setiap server dinyalakan ulang. Isi dari pengelola secret untuk produksi.")
	return buf
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

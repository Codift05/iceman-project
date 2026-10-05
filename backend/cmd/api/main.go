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

	"github.com/iceman/backend/internal/audit"
	"github.com/iceman/backend/internal/cart"
	"github.com/iceman/backend/internal/catalog"
	"github.com/iceman/backend/internal/customer"
	"github.com/iceman/backend/internal/httpx"
	"github.com/iceman/backend/internal/identity"
	"github.com/iceman/backend/internal/order"
	"github.com/iceman/backend/internal/scheduling"
	"github.com/iceman/backend/internal/store"
	"github.com/iceman/backend/internal/worker"
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
	recordDenial := func(c echo.Context, permission string) {
		ctx := c.Request().Context()
		err := audit.RecordOutside(ctx, pool, audit.Entry{
			Entity:  "access",
			Action:  audit.ActionAccess,
			Outcome: audit.OutcomeDenied,
			Detail:  c.Request().Method + " " + c.Path() + " butuh " + permission,
		})
		if err != nil {
			log.Error("mencatat penolakan akses gagal", "error", err)
		}
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

	// Penjadwalan: depo, area layanan, dan slot pengiriman.
	sched := scheduling.NewHandler(
		scheduling.NewDepots(pool),
		scheduling.NewAreas(pool),
		scheduling.NewSlots(pool),
	)

	// Endpoint terbuka, dipakai aplikasi pelanggan sebelum masuk untuk
	// memeriksa jangkauan layanan dan melihat jadwal yang tersedia. Hanya
	// berisi data yang memang perlu diketahui calon pelanggan.
	pub := v1.Group("/public")
	pub.GET("/depots/nearest", sched.NearestDepot)
	pub.GET("/areas/:id/availability", sched.Availability)
	pub.GET("/areas/:id/slots/next", sched.NextAvailable)

	izin := func(permission string) echo.MiddlewareFunc {
		return httpx.RequirePermission(checkPermission, permission, recordDenial)
	}

	secured.GET("/depots", sched.ListDepots, izin("depot.view"))
	secured.GET("/depots/:id", sched.GetDepot, izin("depot.view"))
	secured.POST("/depots", sched.CreateDepot, izin("depot.manage"))
	secured.PATCH("/depots/:id", sched.UpdateDepot, izin("depot.manage"))

	secured.GET("/areas", sched.ListAreas, izin("area_slot.view"))
	secured.POST("/areas", sched.CreateArea, izin("area_slot.manage"))
	secured.PATCH("/areas/:id", sched.UpdateArea, izin("area_slot.manage"))

	secured.GET("/areas/:id/slots", sched.ListSlots, izin("area_slot.view"))
	secured.POST("/slots", sched.CreateSlot, izin("area_slot.manage"))
	secured.PATCH("/slots/:id/capacity", sched.SetSlotCapacity, izin("area_slot.manage"))
	secured.PATCH("/slots/:id/holiday", sched.SetSlotHoliday, izin("area_slot.manage"))

	// Katalog, pelanggan, keranjang, dan pesanan.
	//
	// Antrean job dipakai checkout untuk mengantre pembuatan tagihan dalam
	// transaksi yang sama (AD-03). Bila antreannya gagal disiapkan, server
	// tetap jalan namun pesanan baru tidak akan punya tagihan, jadi itu
	// dicatat sebagai peringatan yang menonjol.
	queue, err := worker.NewClient(pool)
	if err != nil {
		log.Error("klien antrean gagal disiapkan, pembuatan tagihan tidak akan diantre",
			"error", err)
	}

	products := catalog.NewProducts(pool)
	customers := customer.NewCustomers(pool)
	carts := cart.NewCarts(pool, products)
	slots := scheduling.NewSlots(pool)
	orders := order.NewOrders(order.Deps{
		Pool: pool, Carts: carts, Customers: customers, Slots: slots, Queue: queue,
	})

	catalogHandler := catalog.NewHandler(products)
	customerHandler := customer.NewHandler(customers)
	orderHandler := order.NewHandler(orders, carts)

	// Katalog pelanggan terbuka tanpa token, seperti endpoint publik lain:
	// calon pelanggan perlu melihat produk dan harganya sebelum masuk.
	pub.GET("/products", catalogHandler.Catalog)

	secured.GET("/products", catalogHandler.List, izin("product.view"))
	secured.GET("/products/:id", catalogHandler.Get, izin("product.view"))
	secured.POST("/products", catalogHandler.Create, izin("product.manage"))
	secured.PATCH("/products/:id", catalogHandler.Update, izin("product.manage"))
	secured.PUT("/products/:id/contract-price", catalogHandler.SetContractPrice,
		izin("product.manage"))

	secured.GET("/customers", customerHandler.List, izin("customer.view"))
	secured.GET("/customers/:id", customerHandler.Get, izin("customer.view"))
	secured.POST("/customers", customerHandler.Create, izin("customer.manage"))
	secured.PATCH("/customers/:id", customerHandler.Update, izin("customer.manage"))
	secured.GET("/customers/:id/addresses", customerHandler.ListAddresses,
		izin("customer.view"))
	secured.POST("/customers/:id/addresses", customerHandler.AddAddress,
		izin("customer.manage"))
	secured.PUT("/customers/:id/addresses/:addressID/primary",
		customerHandler.SetPrimaryAddress, izin("customer.manage"))
	secured.DELETE("/customers/:id/addresses/:addressID",
		customerHandler.DeactivateAddress, izin("customer.manage"))

	// Termin kontrak menentukan apakah pesanan boleh melewati pembayaran di
	// muka, sehingga izinnya dipisahkan dari pengelolaan pelanggan biasa.
	secured.PUT("/customers/:id/contract-term", customerHandler.SetContractTerm,
		izin("customer.manage_terms"))

	// Keranjang pelanggan dikelola admin, untuk pesanan yang masuk lewat
	// telepon. Endpoint pelanggan sendiri menunggu keputusan OQ-002 tentang
	// cara pelanggan masuk.
	secured.GET("/customers/:id/cart", orderHandler.GetCart, izin("order.create_manual"))
	secured.POST("/customers/:id/cart/items", orderHandler.AddToCart,
		izin("order.create_manual"))
	secured.PUT("/customers/:id/cart/items/:productID", orderHandler.SetCartQty,
		izin("order.create_manual"))
	secured.POST("/customers/:id/orders/:orderID/reorder", orderHandler.Reorder,
		izin("order.create_manual"))

	secured.GET("/orders", orderHandler.List, izin("order.view"))
	secured.GET("/orders/:id", orderHandler.Get, izin("order.view"))
	secured.GET("/orders/:id/history", orderHandler.History, izin("order.view"))
	secured.POST("/orders", orderHandler.Checkout, izin("order.create_manual"))
	secured.POST("/orders/:id/status", orderHandler.ChangeStatus, izin("order.manage"))
	secured.POST("/orders/:id/cancel", orderHandler.Cancel, izin("order.manage"))
	secured.POST("/orders/:id/reschedule", orderHandler.Reschedule,
		izin(order.ReschedulePermission()))

	// Penelusuran jejak audit, hanya untuk peran yang berwenang.
	auditReader := audit.NewReader(pool)
	secured.GET("/admin/audit-trail", func(c echo.Context) error {
		rows, err := auditReader.List(c.Request().Context(), audit.Filter{
			Entity:  c.QueryParam("entity"),
			Outcome: c.QueryParam("outcome"),
		})
		if err != nil {
			return httpx.Fail(c, "INTERNAL")
		}
		return c.JSON(http.StatusOK, map[string]any{"entries": rows})
	}, httpx.RequirePermission(checkPermission, "audit.view", recordDenial))

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

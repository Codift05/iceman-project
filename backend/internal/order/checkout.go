package order

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/iceman/backend/internal/audit"
	"github.com/iceman/backend/internal/cart"
	"github.com/iceman/backend/internal/customer"
	"github.com/iceman/backend/internal/scheduling"
	"github.com/iceman/backend/internal/worker"
)

// Deps adalah apa yang dibutuhkan pembuatan pesanan.
type Deps struct {
	Pool      *pgxpool.Pool
	Carts     *cart.Carts
	Customers *customer.Customers
	// Queue boleh kosong. Bila kosong, job susulan tidak diantre dan
	// pembuatan pesanan tetap berhasil. Itu dipakai uji yang tidak
	// memerlukan antrean, bukan keadaan produksi.
	Queue *river.Client[pgx.Tx]
}

// Orders menangani pembuatan dan pengelolaan pesanan.
type Orders struct{ d Deps }

// NewOrders membuat pengelola pesanan.
func NewOrders(d Deps) *Orders { return &Orders{d: d} }

// CheckoutInput adalah permintaan pembuatan pesanan.
type CheckoutInput struct {
	CustomerID uuid.UUID
	AddressID  uuid.UUID
	SlotID     uuid.UUID
	Notes      string

	// IdempotencyKey membuat permintaan berulang menghasilkan satu pesanan
	// (SRS-ORD-002). Boleh kosong, namun klien seluler sebaiknya mengisinya.
	IdempotencyKey string

	// Channel dan CreatedBy terisi untuk pesanan manual oleh admin
	// (SRS-ORD-006). Kosong berarti pesanan dari aplikasi pelanggan.
	Channel   string
	CreatedBy *uuid.UUID
}

// Checkout membuat pesanan dari keranjang pelanggan.
//
// Urutan kerjanya mengikuti dua diagram alur checkout pada SRS Bab 4.3.
// Seluruh pemeriksaan yang tidak memerlukan kunci dikerjakan lebih dahulu, di
// luar transaksi. Baru setelah semuanya lolos, transaksi dimulai dan baris
// slot dikunci. Mengunci lebih awal berarti permintaan yang jelas salah ikut
// menahan pelanggan lain yang mengincar slot yang sama.
//
// Di dalam transaksi, lima hal terjadi bersama: kuota slot diambil, pesanan
// dan isinya disimpan, keranjang dikosongkan, riwayat status dicatat, dan job
// susulan diantre. Semuanya berhasil bersama atau gagal bersama (AD-03).
func (o *Orders) Checkout(ctx context.Context, in CheckoutInput) (*Order, error) {
	now := time.Now()

	// --- Tahap satu: pemeriksaan tanpa kunci (SRS Gambar alur checkout A) ---

	// Permintaan berulang dijawab dengan pesanan yang sudah ada, sebelum
	// pekerjaan apa pun dimulai. Jaringan seluler sering memutus sambungan
	// sesudah permintaan terkirim, sehingga klien mengirim ulang permintaan
	// yang sebenarnya sudah berhasil.
	if in.IdempotencyKey != "" {
		ada, err := o.byIdempotencyKey(ctx, in.CustomerID, in.IdempotencyKey)
		if err != nil {
			return nil, err
		}
		if ada != nil {
			return ada, nil
		}
	}

	keranjang, err := o.d.Carts.Get(ctx, in.CustomerID)
	if err != nil {
		return nil, err
	}
	if keranjang.UsableCount == 0 && keranjang.BlockedCount == 0 {
		// Keranjang kosong dapat berarti permintaan kembar ini sudah berhasil
		// dikerjakan oleh permintaan sebelumnya, yang mengosongkan keranjang
		// saat menyimpan pesanan. Kunci diperiksa ulang di sini karena
		// pemeriksaan di atas berjalan sebelum permintaan itu commit, dan
		// pembacaan keranjang ini berjalan sesudahnya.
		//
		// Tanpa pemeriksaan ulang ini, klien yang mengirim ulang permintaannya
		// menerima "keranjang kosong" untuk pesanan yang sebenarnya berhasil.
		if in.IdempotencyKey != "" {
			ada, errBaca := o.byIdempotencyKey(ctx, in.CustomerID, in.IdempotencyKey)
			if errBaca != nil {
				return nil, errBaca
			}
			if ada != nil {
				return ada, nil
			}
		}
		return nil, ErrCartEmpty
	}
	// Item yang terhalang menghentikan checkout, bukan diam diam dibuang.
	// Pelanggan harus memutuskan sendiri apakah pesanan tanpa item itu masih
	// sesuai keinginannya.
	if keranjang.BlockedCount > 0 {
		return nil, ErrItemsUnavailable
	}

	alamat, err := o.d.Customers.ResolveForDelivery(ctx, in.CustomerID, in.AddressID)
	if err != nil {
		return nil, err
	}

	// Slot wajib melayani wilayah alamat. Tanpa pemeriksaan ini, pelanggan
	// dapat mengambil kuota slot milik wilayah lain, dan deponya kebagian
	// pesanan yang tidak dapat diantarnya.
	var slotArea uuid.UUID
	err = o.d.Pool.QueryRow(ctx,
		`SELECT service_area_id FROM delivery_slots WHERE id = $1`, in.SlotID).Scan(&slotArea)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, scheduling.ErrSlotNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("memeriksa wilayah slot: %w", err)
	}
	if slotArea != alamat.ServiceAreaID {
		return nil, ErrSlotAreaMismatch
	}

	bayarBelakangan, term, err := o.d.Customers.PaysOnTerms(ctx, in.CustomerID)
	if err != nil {
		return nil, err
	}

	subtotal := keranjang.SubtotalCents
	ongkos := alamat.DeliveryFeeCents
	// Promo belum diterapkan. SRS-CAT-003 berstatus Should Have, dan diskon
	// tetap nol sampai aturannya diputuskan. Nilainya dihitung di sini, bukan
	// di basis data, agar DB-08 tetap dapat memeriksa konsistensi totalnya.
	var diskon int64
	total := subtotal + ongkos - diskon

	kanal := in.Channel
	if kanal == "" {
		kanal = ChannelApp
	}

	// Pelanggan kontrak bertermin melewati pembayaran di muka (BR-002).
	status := StatusWaitingPayment
	var termDays *int32
	if bayarBelakangan {
		status = StatusProcessing
		termDays = &term.PaymentTermDays
	}

	// --- Tahap dua: transaksi slot dan penyimpanan (SRS Gambar alur checkout B) ---

	tx, err := o.d.Pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("memulai transaksi: %w", err)
	}
	defer tx.Rollback(ctx)

	// Mengunci dan mengambil kuota slot. Fungsi ini yang memeriksa hari libur,
	// batas pemesanan, dan sisa kuota, memakai SELECT FOR UPDATE.
	if _, err := scheduling.Reserve(ctx, tx, in.SlotID, now); err != nil {
		return nil, err
	}

	var (
		pesanan Order
		tanggal time.Time
	)
	err = tx.QueryRow(ctx, `
		INSERT INTO orders (
			order_no, customer_id, depot_id, service_area_id, address_id,
			recipient_name, recipient_phone, address_line, address_notes,
			latitude, longitude, slot_id, scheduled_date, status, channel,
			subtotal_cents, delivery_fee_cents, discount_cents, total_cents,
			payment_term_days, idempotency_key, created_by, customer_notes)
		SELECT 'ICE-' || to_char(now(), 'YYMMDD') || '-' ||
		       lpad(nextval('order_no_seq')::text, 5, '0'),
		       $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11,
		       s.slot_date, $12::order_status, $13::order_channel,
		       $14, $15, $16, $17, $18, nullif($19, ''), $20, $21
		FROM   delivery_slots s WHERE s.id = $11
		RETURNING id, order_no, status::text, channel::text, scheduled_date, created_at`,
		in.CustomerID, alamat.DepotID, alamat.ServiceAreaID, alamat.ID,
		alamat.RecipientName, alamat.Phone, alamat.AddressLine, alamat.Notes,
		alamat.Latitude, alamat.Longitude, in.SlotID,
		status, kanal, subtotal, ongkos, diskon, total,
		termDays, in.IdempotencyKey, in.CreatedBy, strings.TrimSpace(in.Notes)).
		Scan(&pesanan.ID, &pesanan.OrderNo, &pesanan.Status, &pesanan.Channel,
			&tanggal, &pesanan.CreatedAt)
	// Permintaan kembar yang sampai bersamaan baru bertemu di sini. Pemeriksaan
	// kunci di awal fungsi ini dilewati keduanya sebelum salah satu menyimpan,
	// sehingga yang menjaganya adalah indeks keunikan pada basis data. Yang
	// kalah membaca pesanan milik yang menang dan mengembalikannya, bukan
	// meneruskan galat basis data, karena dari sisi klien permintaannya memang
	// berhasil (SRS-ORD-002).
	if isUniqueViolation(err) && in.IdempotencyKey != "" {
		_ = tx.Rollback(ctx)
		ada, errBaca := o.byIdempotencyKey(ctx, in.CustomerID, in.IdempotencyKey)
		if errBaca != nil {
			return nil, errBaca
		}
		if ada != nil {
			return ada, nil
		}
		return nil, fmt.Errorf("menyimpan pesanan: %w", err)
	}
	if err != nil {
		return nil, fmt.Errorf("menyimpan pesanan: %w", err)
	}

	// Harga, nama, dan kemasan disalin dari keranjang yang baru saja dihitung,
	// bukan dibaca ulang dari produk. Membacanya ulang membuka celah: harga
	// dapat berubah antara penghitungan subtotal dan penyimpanan item, dan
	// totalnya tidak lagi cocok dengan rinciannya.
	for _, it := range keranjang.Items {
		_, err := tx.Exec(ctx, `
			INSERT INTO order_items
			       (order_id, product_id, sku, name, packaging, qty,
			        unit_price_cents, line_total_cents)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			pesanan.ID, it.ProductID, it.SKU, it.Name, it.Packaging, it.Qty,
			it.UnitPriceCents, it.LineTotalCents)
		if err != nil {
			return nil, fmt.Errorf("menyimpan item pesanan: %w", err)
		}
		pesanan.Items = append(pesanan.Items, Item{
			ProductID: it.ProductID, SKU: it.SKU, Name: it.Name,
			Packaging: it.Packaging, Qty: it.Qty,
			UnitPriceCents: it.UnitPriceCents, LineTotalCents: it.LineTotalCents,
		})
	}

	if err := catatStatus(ctx, tx, pesanan.ID, nil, status, actorFrom(ctx), ""); err != nil {
		return nil, err
	}

	// Keranjang dikosongkan di dalam transaksi yang sama. Keranjang yang
	// terkosongkan padahal pesanannya batal berarti pelanggan kehilangan
	// pilihannya tanpa mendapat apa pun.
	if err := o.d.Carts.ClearTx(ctx, tx, in.CustomerID); err != nil {
		return nil, err
	}

	if err := audit.Record(ctx, tx, audit.Entry{
		Entity: "orders", EntityID: &pesanan.ID, Action: audit.ActionCreate,
		After: map[string]any{
			"order_no": pesanan.OrderNo, "status": status,
			"total_cents": total, "channel": kanal,
		},
		Detail: fmt.Sprintf("pesanan dibuat lewat %s, total %d sen", kanal, total),
	}); err != nil {
		return nil, err
	}

	// Pembuatan QRIS diantre dalam transaksi yang sama (SRS-ORD-002). Pesanan
	// yang tersimpan tanpa tagihan membuat pelanggan menunggu pembayaran yang
	// tidak pernah muncul.
	if status == StatusWaitingPayment && o.d.Queue != nil {
		err := worker.EnqueueTx(ctx, o.d.Queue, tx, worker.CreatePaymentArgs{
			OrderID:    pesanan.ID,
			TotalCents: total,
		})
		if err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("menyimpan pesanan: %w", err)
	}

	pesanan.CustomerID = in.CustomerID
	pesanan.DepotID = alamat.DepotID
	pesanan.ServiceAreaID = alamat.ServiceAreaID
	pesanan.RecipientName = alamat.RecipientName
	pesanan.RecipientPhone = alamat.Phone
	pesanan.AddressLine = alamat.AddressLine
	pesanan.AddressNotes = alamat.Notes
	pesanan.Latitude = alamat.Latitude
	pesanan.Longitude = alamat.Longitude
	pesanan.SlotID = in.SlotID
	pesanan.ScheduledDate = tanggal.Format("2006-01-02")
	pesanan.SubtotalCents = subtotal
	pesanan.DeliveryFeeCents = ongkos
	pesanan.DiscountCents = diskon
	pesanan.TotalCents = total
	pesanan.PaymentTermDays = termDays
	pesanan.CustomerNotes = strings.TrimSpace(in.Notes)
	return &pesanan, nil
}

// byIdempotencyKey mencari pesanan yang sudah dibuat dengan kunci yang sama.
func (o *Orders) byIdempotencyKey(ctx context.Context, customerID uuid.UUID, key string) (*Order, error) {
	var id uuid.UUID
	err := o.d.Pool.QueryRow(ctx, `
		SELECT id FROM orders
		WHERE  customer_id = $1 AND idempotency_key = $2`, customerID, key).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("memeriksa kunci idempotensi: %w", err)
	}
	return o.Get(ctx, id)
}

// catatStatus menulis satu baris riwayat status di dalam transaksi pemanggil.
func catatStatus(ctx context.Context, tx pgx.Tx, orderID uuid.UUID, dari *string, ke string, actor *uuid.UUID, alasan string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO order_status_history (order_id, from_status, to_status, actor_id, reason)
		VALUES ($1, $2::order_status, $3::order_status, $4, $5)`,
		orderID, dari, ke, actor, alasan)
	if err != nil {
		return fmt.Errorf("mencatat riwayat status: %w", err)
	}
	return nil
}

// actorFrom membaca pelaku dari konteks. Pesanan dari aplikasi pelanggan tidak
// punya pelaku internal, sehingga nil adalah keadaan yang sah.
func actorFrom(ctx context.Context) *uuid.UUID {
	return audit.ActorFrom(ctx)
}

// isUniqueViolation mengenali pelanggaran kekangan keunikan dari PostgreSQL.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

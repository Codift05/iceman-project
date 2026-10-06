package order

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/iceman/backend/internal/audit"
	"github.com/iceman/backend/internal/notify"
	"github.com/iceman/backend/internal/scheduling"
)

// ChangeInput adalah data perubahan status.
type ChangeInput struct {
	// Reason wajib untuk perpindahan yang menandainya begitu, misalnya
	// pembatalan dan kegagalan kirim.
	Reason string
}

// ChangeStatus memindahkan pesanan ke status lain.
//
// Perpindahan yang tidak terdaftar pada matriks ditolak, termasuk bila dipicu
// dari dashboard admin (SRS-ORD-003). Efek sampingnya dikerjakan dalam
// transaksi yang sama dengan perubahan statusnya, sehingga tidak mungkin ada
// pesanan yang batal tanpa kuota slotnya kembali, maupun kuota yang kembali
// untuk pesanan yang ternyata tidak batal.
func (o *Orders) ChangeStatus(ctx context.Context, orderID uuid.UUID, to string, in ChangeInput) (*Order, error) {
	tx, err := o.d.Pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("memulai transaksi: %w", err)
	}
	defer tx.Rollback(ctx)

	// Baris pesanan dikunci lebih dahulu. Tanpa kunci, dua admin yang menekan
	// tombol bersamaan dapat sama sama membaca status lama lalu keduanya
	// merasa perpindahannya sah, dan efek sampingnya terjadi dua kali. Untuk
	// pembatalan itu berarti kuota slot dikembalikan dua kali.
	var (
		dari   string
		slotID uuid.UUID
	)
	err = tx.QueryRow(ctx, `
		SELECT status::text, slot_id FROM orders WHERE id = $1 FOR UPDATE`, orderID).
		Scan(&dari, &slotID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("mengunci pesanan: %w", err)
	}

	t, ok := Allowed(dari, to)
	if !ok {
		return nil, fmt.Errorf("%w: %s ke %s", ErrInvalidTransition, dari, to)
	}

	alasan := strings.TrimSpace(in.Reason)
	if t.RequiresReason && alasan == "" {
		return nil, ErrReasonRequired
	}

	// Kuota dikembalikan sebelum status diubah, agar kekangan yang mewajibkan
	// alasan pembatalan terisi tidak pernah terlihat setengah jalan.
	if t.ReleasesSlot {
		if err := scheduling.Release(ctx, tx, slotID); err != nil {
			return nil, err
		}
	}

	var selesai *time.Time
	if t.MarksCompleted {
		n := time.Now()
		selesai = &n
	}

	_, err = tx.Exec(ctx, `
		UPDATE orders
		SET    status = $2::order_status,
		       cancelled_reason = CASE WHEN $2 = 'CANCELLED' THEN $3 ELSE cancelled_reason END,
		       completed_at = coalesce($4, completed_at)
		WHERE  id = $1`, orderID, to, alasan, selesai)
	if err != nil {
		return nil, fmt.Errorf("mengubah status pesanan: %w", err)
	}

	pelaku := audit.ActorFrom(ctx)
	if err := catatStatus(ctx, tx, orderID, &dari, to, pelaku, alasan); err != nil {
		return nil, err
	}

	if err := audit.Record(ctx, tx, audit.Entry{
		Entity: "orders", EntityID: &orderID, Action: audit.ActionUpdate,
		Before: map[string]any{"status": dari},
		After:  map[string]any{"status": to},
		Detail: rinciTransisi(t, alasan),
	}); err != nil {
		return nil, err
	}

	// Notifikasi diantre dalam transaksi yang sama, sehingga tidak mungkin ada
	// pemberitahuan tentang perpindahan yang ternyata batal. Inilah efek
	// samping "Antre notifikasi" pada tabel transisi SRS Bab 5.1.
	if event := notifyEvent(to); event != "" && o.d.Notifier != nil {
		if err := o.d.Notifier.EmitForOrderTx(ctx, tx, event, orderID,
			alasanUntukPelanggan(to, alasan)); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("mengubah status pesanan: %w", err)
	}
	return o.Get(ctx, orderID)
}

func rinciTransisi(t Transition, alasan string) string {
	bagian := []string{fmt.Sprintf("status %s menjadi %s", t.From, t.To)}
	if t.ReleasesSlot {
		bagian = append(bagian, "kuota slot dikembalikan")
	}
	if alasan != "" {
		bagian = append(bagian, "alasan: "+alasan)
	}
	return strings.Join(bagian, ", ")
}

// Cancel membatalkan pesanan beserta alasannya.
//
// Pembungkus tipis di atas ChangeStatus, disediakan karena pembatalan adalah
// tindakan yang paling sering dipanggil dan alasannya wajib. Dengan nama
// tersendiri, pemanggil tidak dapat lupa bahwa alasannya diperlukan.
func (o *Orders) Cancel(ctx context.Context, orderID uuid.UUID, alasan string) (*Order, error) {
	if strings.TrimSpace(alasan) == "" {
		return nil, ErrReasonRequired
	}
	return o.ChangeStatus(ctx, orderID, StatusCancelled, ChangeInput{Reason: alasan})
}

// Reschedule memindahkan pesanan ke slot lain.
//
// Kuota dipindahkan dari slot lama ke slot baru dalam satu transaksi, dan slot
// baru harus memenuhi seluruh syarat yang sama seperti pemesanan awal: bukan
// hari libur, belum melewati batas pemesanan, dan masih punya kuota
// (SRS-ORD-004).
//
// Statusnya tidak berubah. Yang berpindah adalah slotnya, sehingga ini bukan
// perpindahan status walau tabel SRS mencantumkannya sebagai SCHEDULED ke
// SCHEDULED.
func (o *Orders) Reschedule(ctx context.Context, orderID, slotBaru uuid.UUID, alasan string) (*Order, error) {
	now := time.Now()

	tx, err := o.d.Pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("memulai transaksi: %w", err)
	}
	defer tx.Rollback(ctx)

	var (
		status   string
		slotLama uuid.UUID
		areaID   uuid.UUID
	)
	err = tx.QueryRow(ctx, `
		SELECT status::text, slot_id, service_area_id
		FROM   orders WHERE id = $1 FOR UPDATE`, orderID).
		Scan(&status, &slotLama, &areaID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("mengunci pesanan: %w", err)
	}

	// Hanya pesanan yang sudah terjadwal dapat dijadwalkan ulang. Pesanan yang
	// masih menunggu pembayaran belum punya jadwal untuk dipindahkan, dan
	// pesanan yang sudah selesai atau batal tidak boleh diubah lagi.
	if status != StatusScheduled && status != StatusProcessing {
		return nil, fmt.Errorf("%w: penjadwalan ulang dari status %s", ErrInvalidTransition, status)
	}

	// Slot tujuan wajib melayani wilayah yang sama. Memindahkan pesanan ke
	// wilayah lain berarti deponya berubah, dan itu bukan penjadwalan ulang
	// melainkan pesanan yang berbeda.
	var areaBaru uuid.UUID
	err = tx.QueryRow(ctx,
		`SELECT service_area_id FROM delivery_slots WHERE id = $1`, slotBaru).Scan(&areaBaru)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, scheduling.ErrSlotNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("memeriksa wilayah slot tujuan: %w", err)
	}
	if areaBaru != areaID {
		return nil, ErrSlotAreaMismatch
	}

	if slotLama == slotBaru {
		return nil, fmt.Errorf("%w: slot tujuan sama dengan slot sekarang", ErrInvalidTransition)
	}

	// Move mengambil kuota slot tujuan lebih dahulu, baru mengembalikan kuota
	// slot asal. Urutan itu penting: bila slot tujuan ternyata penuh, kuota
	// slot asal belum dilepas, sehingga pesanan tidak kehilangan jadwalnya.
	if err := scheduling.Move(ctx, tx, slotLama, slotBaru, now); err != nil {
		return nil, err
	}

	_, err = tx.Exec(ctx, `
		UPDATE orders
		SET    slot_id = $2,
		       scheduled_date = (SELECT slot_date FROM delivery_slots WHERE id = $2)
		WHERE  id = $1`, orderID, slotBaru)
	if err != nil {
		return nil, fmt.Errorf("memindahkan jadwal pesanan: %w", err)
	}

	pelaku := audit.ActorFrom(ctx)
	if err := catatStatus(ctx, tx, orderID, &status, status, pelaku,
		"dijadwalkan ulang"+alasanTambahan(alasan)); err != nil {
		return nil, err
	}

	if err := audit.Record(ctx, tx, audit.Entry{
		Entity: "orders", EntityID: &orderID, Action: audit.ActionUpdate,
		Before: map[string]any{"slot_id": slotLama},
		After:  map[string]any{"slot_id": slotBaru},
		Detail: "jadwal dipindahkan ke slot lain" + alasanTambahan(alasan),
	}); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("memindahkan jadwal pesanan: %w", err)
	}
	return o.Get(ctx, orderID)
}

func alasanTambahan(alasan string) string {
	alasan = strings.TrimSpace(alasan)
	if alasan == "" {
		return ""
	}
	return ", alasan: " + alasan
}

// notifyEvent memetakan status pesanan ke event notifikasi.
//
// Hanya status yang berarti bagi pelanggan yang memicu notifikasi. Perpindahan
// seperti PAID ke PROCESSING adalah urusan internal, dan memberitahukannya
// hanya membuat pelanggan menerima pesan yang tidak menuntut tindakan apa pun.
func notifyEvent(status string) string {
	switch status {
	case StatusCancelled:
		return notify.EventOrderCancelled
	default:
		return ""
	}
}

// alasanUntukPelanggan memilih alasan yang layak disampaikan kepada pelanggan.
//
// Alasan pembatalan disampaikan karena pelanggan berhak tahu sebabnya, dan
// tanpa itu ia akan menelepon untuk menanyakannya. Alasan pada perpindahan
// lain adalah catatan internal dan tidak diteruskan.
func alasanUntukPelanggan(status, alasan string) string {
	if status == StatusCancelled && alasan != "" {
		return "Alasan: " + alasan + "."
	}
	return ""
}

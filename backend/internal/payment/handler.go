package payment

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/iceman/backend/internal/httpx"
	"github.com/iceman/backend/internal/order"
)

// MaxWebhookBody adalah batas ukuran badan webhook yang dibaca.
//
// Dibatasi karena badan permintaan dibaca seluruhnya ke memori untuk
// diverifikasi, dan tanpa batas siapa pun yang tahu alamatnya dapat
// menghabiskan memori server dengan satu permintaan besar.
const MaxWebhookBody = 1 << 20 // 1 MiB

// MaxSettlementFile adalah batas ukuran berkas settlement yang dibaca.
//
// Berkas settlement satu bulan untuk ribuan transaksi masih jauh di bawah
// batas ini, sedangkan tanpa batas satu unggahan besar dapat menghabiskan
// memori server.
const MaxSettlementFile = 16 << 20 // 16 MiB

// EventQueuer mengantre pemrosesan event webhook.
//
// Dinyatakan sebagai antarmuka agar paket ini tidak mengimpor paket pekerja,
// yang akan membentuk lingkaran lewat paket pesanan.
type EventQueuer interface {
	QueuePaymentEvent(r *http.Request, eventRowID uuid.UUID) error
}

// Handler memaparkan pembayaran sebagai endpoint HTTP.
type Handler struct {
	svc      *Service
	verifier Verifier
	queue    EventQueuer
}

// NewHandler membuat handler pembayaran.
//
// Verifier wajib diberikan. Membiarkannya kosong akan membuat webhook terbuka
// bagi siapa pun, jadi handler menolak seluruh webhook bila belum disetel.
func NewHandler(svc *Service, v Verifier, q EventQueuer) *Handler {
	return &Handler{svc: svc, verifier: v, queue: q}
}

// Webhook menerima notifikasi dari penyedia pembayaran.
//
// Urutannya mengikuti SRS-PAY-002 dan tidak boleh ditukar: badan dibaca, tanda
// tangan diverifikasi, baru muatannya dipercaya. Memetakan muatan lebih dahulu
// berarti kita sudah mengolah data yang belum terbukti berasal dari penyedia.
//
// Handler menjawab cepat dan menyerahkan pemrosesan kepada pekerja latar.
func (h *Handler) Webhook(c echo.Context) error {
	req := c.Request()
	body, err := io.ReadAll(io.LimitReader(req.Body, MaxWebhookBody))
	if err != nil {
		return httpx.Fail(c, "VALIDATION_FAILED")
	}

	if h.verifier == nil {
		// Menolak, bukan menerima. Webhook tanpa verifikasi berarti siapa pun
		// yang tahu alamatnya dapat menyatakan pesanan sudah dibayar.
		h.catatPenolakan(c, "verifikator webhook belum disetel", body)
		return httpx.Fail(c, "SIGNATURE_INVALID")
	}
	if err := h.verifier.Verify(body, headerMap(req.Header)); err != nil {
		h.catatPenolakan(c, err.Error(), body)
		return httpx.Fail(c, "SIGNATURE_INVALID")
	}

	in, err := h.uraikan(body)
	if err != nil {
		// Muatan yang tanda tangannya sah namun bentuknya tidak dikenali tetap
		// dicatat sebagai penolakan, karena itu pertanda bentuk muatan
		// penyedia berubah dan perlu diketahui.
		h.catatPenolakan(c, "bentuk muatan tidak dikenali: "+err.Error(), body)
		return httpx.Fail(c, "VALIDATION_FAILED")
	}

	eventRowID, duplikat, err := h.svc.AcceptWebhook(req.Context(), *in)
	if err != nil {
		return httpx.Fail(c, "INTERNAL")
	}
	if duplikat {
		// Event duplikat dijawab berhasil tanpa efek samping (SRS-PAY-002).
		// Menjawabnya dengan galat membuat penyedia mengirim ulang terus.
		return c.JSON(http.StatusOK, map[string]any{"status": "duplikat"})
	}

	if h.queue != nil {
		if err := h.queue.QueuePaymentEvent(req, eventRowID); err != nil {
			// Event sudah tersimpan, jadi jawabannya tetap berhasil: penyedia
			// tidak perlu mengirim ulang. Yang gagal hanya pengantrean, dan
			// event yang belum diproses tetap dapat ditemukan dari basis data.
			return c.JSON(http.StatusOK, map[string]any{
				"status": "diterima",
				"note":   "pemrosesan belum terantre",
			})
		}
	}
	return c.JSON(http.StatusOK, map[string]any{"status": "diterima"})
}

// webhookBody adalah bentuk muatan yang diuraikan.
//
// Nama kolomnya dibuat longgar, menerima beberapa ejaan yang umum dipakai
// penyedia, karena penyedianya belum dipilih. Ketika sudah, bentuk ini
// disempitkan agar muatan yang tidak sesuai tertangkap alih alih diterima
// separuh.
type webhookBody struct {
	EventID   string `json:"event_id"`
	ID        string `json:"id"`
	Reference string `json:"reference"`

	OrderRef    string `json:"order_id"`
	ProviderRef string `json:"provider_ref"`
	TxnID       string `json:"transaction_id"`

	Status            string `json:"status"`
	TransactionStatus string `json:"transaction_status"`

	Amount      json.Number `json:"amount"`
	GrossAmount json.Number `json:"gross_amount"`
}

func (h *Handler) uraikan(body []byte) (*WebhookInput, error) {
	var b webhookBody
	if err := json.Unmarshal(body, &b); err != nil {
		return nil, err
	}

	eventID := pilihPertama(b.EventID, b.ID, b.Reference)
	if eventID == "" {
		return nil, errors.New("muatan tanpa pengenal event")
	}
	status := pilihPertama(b.Status, b.TransactionStatus)
	if status == "" {
		return nil, errors.New("muatan tanpa status")
	}

	in := &WebhookInput{
		EventID:        eventID,
		ProviderRef:    pilihPertama(b.ProviderRef, b.TxnID, b.OrderRef),
		ProviderStatus: status,
		Payload:        body,
	}

	// Nominal dibaca sebagai angka desimal dalam rupiah bila mengandung titik,
	// dan sebagai sen bila bulat. Penyedia Indonesia umumnya mengirim rupiah
	// bulat, sehingga nilai bulat dikalikan seratus untuk menjadi sen.
	if n, ok := bacaNominal(pilihPertama(b.Amount.String(), b.GrossAmount.String())); ok {
		in.AmountCents = &n
	}
	return in, nil
}

// bacaNominal menerjemahkan nominal dari muatan menjadi sen.
//
// Penyedia Indonesia umumnya mengirim rupiah bulat, misalnya 25000 untuk dua
// puluh lima ribu rupiah. Nilai berdesimal, misalnya 25000.00, juga diterima
// dan dibaca sebagai rupiah. Keduanya dikonversi menjadi sen agar sebanding
// dengan nominal tagihan yang disimpan dalam sen.
func bacaNominal(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < 0 {
		return 0, false
	}
	// Pembulatan dilakukan setelah dikalikan seratus, bukan sebelumnya, agar
	// nilai seperti 25000.99 tidak kehilangan bagian sennya.
	return int64(f*100 + 0.5), true
}

func pilihPertama(nilai ...string) string {
	for _, v := range nilai {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

func headerMap(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for k := range h {
		out[k] = h.Get(k)
	}
	return out
}

// catatPenolakan mencatat percobaan webhook yang ditolak.
//
// Badannya tidak disimpan, hanya ringkasannya, karena muatan yang belum
// terverifikasi tidak dipercaya. Ringkasan cukup untuk mengenali percobaan
// yang sama dikirim berulang kali.
func (h *Handler) catatPenolakan(c echo.Context, alasan string, body []byte) {
	sum := sha256.Sum256(body)
	_ = h.svc.RecordReject(c.Request().Context(), alasan,
		c.RealIP(), hex.EncodeToString(sum[:]))
}

// --- sisi admin dan keuangan ---

// Charge membuat tagihan untuk sebuah pesanan.
func (h *Handler) Charge(c echo.Context) error {
	orderID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return httpx.Fail(c, "NOT_FOUND")
	}
	p, err := h.svc.Charge(c.Request().Context(), orderID)
	if err != nil {
		return httpx.Fail(c, codeFor(err), detailFor(err)...)
	}
	return c.JSON(http.StatusCreated, p)
}

// ListForOrder mengembalikan pembayaran sebuah pesanan.
func (h *Handler) ListForOrder(c echo.Context) error {
	orderID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return httpx.Fail(c, "NOT_FOUND")
	}
	out, err := h.svc.ListForOrder(c.Request().Context(), orderID)
	if err != nil {
		return httpx.Fail(c, "INTERNAL")
	}
	return c.JSON(http.StatusOK, map[string]any{"payments": out})
}

// List mengembalikan daftar pembayaran untuk keuangan.
func (h *Handler) List(c echo.Context) error {
	f := ListFilter{
		Status: strings.ToUpper(c.QueryParam("status")),
		From:   c.QueryParam("from"),
		Until:  c.QueryParam("until"),
	}
	if raw := c.QueryParam("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			f.Limit = n
		}
	}
	out, err := h.svc.List(c.Request().Context(), f)
	if err != nil {
		return httpx.Fail(c, "INTERNAL")
	}
	return c.JSON(http.StatusOK, map[string]any{"payments": out})
}

// Get mengembalikan satu pembayaran beserta refundnya.
func (h *Handler) Get(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return httpx.Fail(c, "NOT_FOUND")
	}
	ctx := c.Request().Context()

	p, err := h.svc.Get(ctx, id)
	if err != nil {
		return httpx.Fail(c, codeFor(err), detailFor(err)...)
	}
	refunds, err := h.svc.Refunds(ctx, id)
	if err != nil {
		return httpx.Fail(c, "INTERNAL")
	}
	return c.JSON(http.StatusOK, map[string]any{
		"payment":    p,
		"refunds":    refunds,
		"refundable": p.Refundable(),
	})
}

type refundRequest struct {
	AmountCents int64  `json:"amount_cents"`
	Reason      string `json:"reason"`
}

// Refund mengembalikan dana atas sebuah pembayaran.
func (h *Handler) Refund(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return httpx.Fail(c, "NOT_FOUND")
	}
	var req refundRequest
	if err := c.Bind(&req); err != nil {
		return httpx.Fail(c, "VALIDATION_FAILED")
	}

	r, err := h.svc.Refund(c.Request().Context(), id, RefundInput{
		AmountCents: req.AmountCents, Reason: req.Reason,
	})
	if err != nil {
		return httpx.Fail(c, codeFor(err), detailFor(err)...)
	}
	return c.JSON(http.StatusCreated, r)
}

// EventsNeedingReview mengembalikan event yang menunggu tinjauan manusia.
func (h *Handler) EventsNeedingReview(c echo.Context) error {
	batas := 0
	if raw := c.QueryParam("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			batas = n
		}
	}
	out, err := h.svc.EventsNeedingReview(c.Request().Context(), batas)
	if err != nil {
		return httpx.Fail(c, "INTERNAL")
	}
	return c.JSON(http.StatusOK, map[string]any{"events": out})
}

// codeFor menerjemahkan galat domain menjadi kode galat API.
func codeFor(err error) string {
	switch {
	case errors.Is(err, ErrNotFound), errors.Is(err, order.ErrNotFound):
		return "NOT_FOUND"
	case errors.Is(err, ErrAlreadyPaid):
		return "PAYMENT_ALREADY_PAID"
	case errors.Is(err, ErrOrderNotPayable):
		return "ORDER_NOT_PAYABLE"
	case errors.Is(err, ErrProvider):
		return "PAYMENT_PROVIDER_ERROR"
	case errors.Is(err, ErrSignatureInvalid):
		return "SIGNATURE_INVALID"
	case errors.Is(err, ErrNotRefundable):
		return "PAYMENT_NOT_REFUNDABLE"
	case errors.Is(err, ErrRefundExceeds):
		return "REFUND_EXCEEDS_PAYMENT"
	case errors.Is(err, ErrReasonRequired):
		return "REASON_REQUIRED"
	case errors.Is(err, ErrAmountMismatch):
		return "VALIDATION_FAILED"
	case errors.Is(err, ErrFlagNotFound):
		return "NOT_FOUND"
	case errors.Is(err, ErrPeriodInvalid), errors.Is(err, ErrFlagNoSubject),
		errors.Is(err, ErrFeeInvalid):
		return "VALIDATION_FAILED"
	case errors.Is(err, ErrAlreadyFlagged):
		return "CONFLICT"
	default:
		return "INTERNAL"
	}
}

func detailFor(err error) []httpx.Detail {
	switch {
	case errors.Is(err, ErrReasonRequired):
		return []httpx.Detail{{Field: "reason", Message: "Alasan wajib diisi."}}
	case errors.Is(err, ErrRefundExceeds):
		return []httpx.Detail{{Field: "amount_cents", Message: err.Error()}}
	case errors.Is(err, ErrPeriodInvalid):
		return []httpx.Detail{{Field: "from", Message: err.Error()}}
	case errors.Is(err, ErrFlagNoSubject):
		return []httpx.Detail{{Field: "payment_id",
			Message: "Sebutkan pembayaran atau settlement yang ditandai."}}
	case errors.Is(err, ErrFeeInvalid):
		return []httpx.Detail{{Field: "fee_cents", Message: err.Error()}}
	default:
		return nil
	}
}

// --- rekonsiliasi ---

// Reconcile menyusun laporan rekonsiliasi satu periode.
func (h *Handler) Reconcile(c echo.Context) error {
	hasil, err := h.svc.Reconcile(c.Request().Context(), ReconcileFilter{
		From:     c.QueryParam("from"),
		Until:    c.QueryParam("until"),
		OnlyDiff: c.QueryParam("only_diff") == "true",
	})
	if err != nil {
		return httpx.Fail(c, codeFor(err), detailFor(err)...)
	}
	return c.JSON(http.StatusOK, hasil)
}

// ReconcileCSV mengekspor laporan rekonsiliasi sebagai CSV.
//
// Nama berkasnya memuat periodenya, supaya berkas yang sudah diunduh tetap
// dapat dikenali tanpa membukanya.
func (h *Handler) ReconcileCSV(c echo.Context) error {
	f := ReconcileFilter{
		From:     c.QueryParam("from"),
		Until:    c.QueryParam("until"),
		OnlyDiff: c.QueryParam("only_diff") == "true",
	}
	hasil, err := h.svc.Reconcile(c.Request().Context(), f)
	if err != nil {
		return httpx.Fail(c, codeFor(err), detailFor(err)...)
	}

	nama := "rekonsiliasi-" + f.From + "-sampai-" + f.Until + ".csv"
	c.Response().Header().Set(echo.HeaderContentDisposition,
		`attachment; filename="`+nama+`"`)
	c.Response().Header().Set(echo.HeaderContentType, "text/csv; charset=utf-8")
	c.Response().WriteHeader(http.StatusOK)
	if err := hasil.ExportCSV(c.Response()); err != nil {
		// Judul sudah terkirim, jadi galatnya tidak dapat lagi disampaikan
		// sebagai badan JSON. Yang dapat dilakukan hanya menghentikan
		// penulisan; klien akan melihat berkas yang terpotong.
		return err
	}
	return nil
}

// ImportSettlements memasukkan berkas settlement dari penyedia.
//
// Berkas diterima sebagai unggahan multipart, bukan badan JSON, karena yang
// dipegang petugas keuangan adalah berkas CSV dari penyedia apa adanya.
func (h *Handler) ImportSettlements(c echo.Context) error {
	berkas, err := c.FormFile("file")
	if err != nil {
		return httpx.Fail(c, "VALIDATION_FAILED",
			httpx.Detail{Field: "file", Message: "Berkas settlement wajib diunggah."})
	}
	f, err := berkas.Open()
	if err != nil {
		return httpx.Fail(c, "VALIDATION_FAILED",
			httpx.Detail{Field: "file", Message: "Berkas tidak dapat dibaca."})
	}
	defer f.Close()

	rows, ditolak, err := ParseSettlementCSV(io.LimitReader(f, MaxSettlementFile))
	if err != nil {
		return httpx.Fail(c, "VALIDATION_FAILED",
			httpx.Detail{Field: "file", Message: "Berkas bukan CSV yang terbaca."})
	}

	hasil, err := h.svc.ImportSettlements(c.Request().Context(), rows)
	if err != nil {
		return httpx.Fail(c, codeFor(err), detailFor(err)...)
	}
	// Baris yang gagal dibaca dan baris yang gagal disimpan dilaporkan
	// bersama, karena bagi petugas keuangan keduanya sama artinya: baris itu
	// belum masuk dan perlu diperiksa.
	hasil.Rejected = append(ditolak, hasil.Rejected...)
	return c.JSON(http.StatusOK, hasil)
}

type flagRequest struct {
	PaymentID    string `json:"payment_id"`
	SettlementID string `json:"settlement_id"`
	Kind         string `json:"kind"`
	Note         string `json:"note"`
}

// FlagDiscrepancy menandai sebuah selisih untuk ditindaklanjuti.
func (h *Handler) FlagDiscrepancy(c echo.Context) error {
	var req flagRequest
	if err := c.Bind(&req); err != nil {
		return httpx.Fail(c, "VALIDATION_FAILED")
	}

	in := FlagInput{Kind: strings.ToUpper(req.Kind), Note: req.Note}
	if req.PaymentID != "" {
		id, err := uuid.Parse(req.PaymentID)
		if err != nil {
			return httpx.Fail(c, "VALIDATION_FAILED",
				httpx.Detail{Field: "payment_id", Message: "Pengenal pembayaran tidak sah."})
		}
		in.PaymentID = &id
	}
	if req.SettlementID != "" {
		id, err := uuid.Parse(req.SettlementID)
		if err != nil {
			return httpx.Fail(c, "VALIDATION_FAILED",
				httpx.Detail{Field: "settlement_id", Message: "Pengenal settlement tidak sah."})
		}
		in.SettlementID = &id
	}

	f, err := h.svc.FlagDiscrepancy(c.Request().Context(), in)
	if err != nil {
		return httpx.Fail(c, codeFor(err), detailFor(err)...)
	}
	return c.JSON(http.StatusCreated, f)
}

type resolveRequest struct {
	Note string `json:"note"`
}

// ResolveFlag menutup sebuah penandaan selisih.
func (h *Handler) ResolveFlag(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return httpx.Fail(c, "NOT_FOUND")
	}
	var req resolveRequest
	if err := c.Bind(&req); err != nil {
		return httpx.Fail(c, "VALIDATION_FAILED")
	}
	if err := h.svc.ResolveFlag(c.Request().Context(), id, req.Note); err != nil {
		return httpx.Fail(c, codeFor(err), detailFor(err)...)
	}
	return c.NoContent(http.StatusNoContent)
}

// OpenFlags mengembalikan selisih yang belum diselesaikan.
func (h *Handler) OpenFlags(c echo.Context) error {
	batas := 0
	if raw := c.QueryParam("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			batas = n
		}
	}
	out, err := h.svc.OpenFlags(c.Request().Context(), batas)
	if err != nil {
		return httpx.Fail(c, "INTERNAL")
	}
	return c.JSON(http.StatusOK, map[string]any{"flags": out})
}

type feeRequest struct {
	FeeCents int64  `json:"fee_cents"`
	Reason   string `json:"reason"`
}

// CorrectFee mengoreksi biaya penyedia pada sebuah pembayaran.
func (h *Handler) CorrectFee(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return httpx.Fail(c, "NOT_FOUND")
	}
	var req feeRequest
	if err := c.Bind(&req); err != nil {
		return httpx.Fail(c, "VALIDATION_FAILED")
	}
	if err := h.svc.CorrectFee(c.Request().Context(), id, req.FeeCents, req.Reason); err != nil {
		return httpx.Fail(c, codeFor(err), detailFor(err)...)
	}
	return c.NoContent(http.StatusNoContent)
}

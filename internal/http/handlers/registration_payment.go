package handlers

import (
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"marc/internal/db/sqlc"
	"marc/internal/http/middleware"
	"marc/internal/payment"
	"marc/internal/phone"
)

// RegistrationPaymentHandler — yuran pendaftaran ahli SEKALI BAYAR
// (ToyyibPay), BUKAN dues berulang dan BUKAN yuran aktiviti
// (`activities.fee_cents`, cangkuk berasingan yang belum wired — lihat
// TODO.md). Bergantung interface `payment.Gateway` sahaja (padanan
// DonationHandler), walaupun buat masa ni cuma SATU gateway berdaftar
// ("toyyibpay") — swap/tambah gateway lain kelak tak sentuh handler ni.
type RegistrationPaymentHandler struct {
	gw       payment.Gateway
	queries  *sqlc.Queries
	pool     *pgxpool.Pool
	feeCents int64
}

func NewRegistrationPaymentHandler(pool *pgxpool.Pool, gw payment.Gateway, feeCents int) *RegistrationPaymentHandler {
	return &RegistrationPaymentHandler{gw: gw, queries: sqlc.New(pool), pool: pool, feeCents: int64(feeCents)}
}

// Checkout mulakan bayaran yuran pendaftaran untuk ahli LOG MASUK
// sendiri (RequireAuth SAHAJA — bukan RequireApprovedStatus, sebab ahli
// `pending` yang menunggu kelulusan MESTI boleh bayar bila-bila masa
// semasa menunggu, sebelum ATAU selepas management tengok permohonan
// dia; lihat TODO.md keputusan produk 2026-08-15). Rekod
// `registration_payments` status 'pending', gate sebenar (`pending` ->
// `approved`) disemak dalam `ProfileHandler.setMemberStatus`.
// checkoutRequest — Phone PILIHAN: cuma perlu bila `profiles.phone`
// masih kosong (ahli `pending` yang daftar SEBELUM phone jadi wajib di
// `/auth/register`, lihat auth.go). Checkout panggilan pertama (tanpa
// Phone) untuk ahli begini akan ditolak dengan `code: "phone_required"`
// — Flutter papar dialog minta nombor, panggil semula Checkout DENGAN
// Phone diisi.
type checkoutRequest struct {
	Phone string `json:"phone"`
}

func (h *RegistrationPaymentHandler) Checkout(c *gin.Context) {
	if h.gw == nil || !h.gw.Enabled() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "pembayaran yuran pendaftaran belum tersedia"})
		return
	}

	var req checkoutRequest
	// Body kosong sah (kes biasa — ahli yang dah ada phone tak perlu
	// hantar apa-apa) — cuma tolak kalau JSON yang DIHANTAR cacat.
	if c.Request.ContentLength > 0 {
		if !bindJSON(c, &req) {
			return
		}
	}

	ctx := c.Request.Context()
	userID := middleware.UserID(c)

	profile, err := h.queries.GetProfileByUserID(ctx, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal mulakan pembayaran"})
		return
	}

	// Dah approved — tak perlu bayar (grandfathered ATAU dah lepas gate
	// ni sebelum ni). Elak bil ToyyibPay berulang untuk sesuatu yang dah
	// tak relevan.
	if profile.Status == "approved" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "akaun anda sudah diluluskan, tiada yuran pendaftaran perlu dibayar"})
		return
	}

	alreadyPaid, err := h.queries.HasSucceededRegistrationPayment(ctx, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal mulakan pembayaran"})
		return
	}
	if alreadyPaid {
		c.JSON(http.StatusBadRequest, gin.H{"error": "yuran pendaftaran anda sudah dibayar"})
		return
	}

	// billTo WAJIB oleh ToyyibPay (disahkan sandbox — lihat
	// toyyibpay.go) — fallback ke member_id kalau display_name kosong,
	// tak pernah kosong dua-dua sebab member_id sentiasa diisi semasa
	// daftar.
	billTo := ""
	if profile.DisplayName.Valid {
		billTo = profile.DisplayName.String
	}
	if billTo == "" {
		billTo = profile.MemberID
	}

	// billPhone JUGA WAJIB oleh ToyyibPay (disahkan LIVE di staging
	// 2026-08-15). Ahli baharu sentiasa ada phone (wajib di
	// /auth/register), tapi ahli `pending` yang daftar SEBELUM syarat tu
	// mungkin belum ada. TIADA placeholder rekaan lagi (keputusan
	// 2026-08-15, sesi susulan) — minta terus dari ahli semasa proses
	// bayar, dan SIMPAN ke profil supaya tak perlu tanya lagi kali
	// seterusnya.
	//
	// Phone yang TERSIMPAN turut disahkan semula (Opus verify 2026-08-15
	// dedah: baris lama yang ditulis SEBELUM `UpdateMe` disahkan hari ni
	// mungkin bawa nilai cacat cth "abc" — tanpa semakan ni, createBill
	// akan tolak dgn 500 generik dan ahli tersekat kekal, tiada laluan
	// untuk cuba lagi). Phone cacat dilayan SAMA macam phone kosong —
	// jatuh ke laluan minta-semula di bawah.
	billPhone := ""
	if profile.Phone.Valid && profile.Phone.String != "" {
		if normalized, ok := phone.NormalizeMY(profile.Phone.String); ok {
			billPhone = normalized
		}
	}
	if billPhone == "" {
		trimmed := strings.TrimSpace(req.Phone)
		if trimmed == "" {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "sila isi nombor telefon dahulu",
				"code":  "phone_required",
			})
			return
		}
		normalized, ok := phone.NormalizeMY(trimmed)
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "format nombor telefon tidak sah"})
			return
		}
		if _, err := h.queries.UpdateProfile(ctx, sqlc.UpdateProfileParams{
			UserID: userID,
			Phone:  pgtype.Text{String: normalized, Valid: true},
		}); err != nil {
			log.Printf("simpan phone semasa checkout yuran pendaftaran: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal mulakan pembayaran"})
			return
		}
		billPhone = normalized
	}

	metadata := map[string]string{
		"description": "Yuran pendaftaran ahli MARC",
		"reference":   profile.MemberID,
		"billTo":      billTo,
		"billEmail":   profile.Email,
		"billPhone":   billPhone,
	}

	result, err := h.gw.CreatePayment(ctx, payment.CreateParams{
		AmountCents: h.feeCents,
		Currency:    "myr",
		Metadata:    metadata,
	})
	if err != nil {
		log.Printf("%s create payment (registration fee): %v", h.gw.Name(), err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal mulakan pembayaran"})
		return
	}

	if _, err := h.queries.CreateRegistrationPayment(ctx, sqlc.CreateRegistrationPaymentParams{
		UserID:      userID,
		AmountCents: int32(h.feeCents),
		Currency:    "myr",
		Gateway:     h.gw.Name(),
		GatewayRef:  result.GatewayRef,
	}); err != nil {
		log.Printf("create registration payment row: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal mulakan pembayaran"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"redirect_url": result.RedirectURL})
}

// Webhook terima callback ToyyibPay dan update status row
// `registration_payments` berpadanan. Route AWAM, tiada auth —
// keselamatan bergantung SEPENUHNYA pada `gw.VerifyWebhook` (padanan
// pola DonationHandler.Webhook). Gateway dihardcode "toyyibpay" (bukan
// `:gateway` path param macam donations) sebab ciri ni cuma ada SATU
// gateway — diff lagi kecil drpd generalize untuk kes yang tak wujud.
func (h *RegistrationPaymentHandler) Webhook(c *gin.Context) {
	if h.gw == nil || !h.gw.Enabled() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "pembayaran yuran pendaftaran belum tersedia"})
		return
	}

	payload, err := c.GetRawData()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "payload tidak sah"})
		return
	}

	event, err := h.gw.VerifyWebhook(payload, c.Request.Header)
	if err != nil {
		if errors.Is(err, payment.ErrIgnoredEvent) {
			c.JSON(http.StatusOK, gin.H{"ok": true})
			return
		}
		// Kredential belum diisi — endpoint TAK boleh sahkan apa-apa,
		// jadi fail-closed (503), bukan terima event tak disahkan.
		if errors.Is(err, payment.ErrNotConfigured) {
			log.Printf("webhook %s (registration fee): kredential belum dikonfigurasi", h.gw.Name())
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "webhook belum dikonfigurasi"})
			return
		}
		log.Printf("webhook %s (registration fee): verify gagal: %v", h.gw.Name(), err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "signature tidak sah"})
		return
	}

	// Race dengan kelulusan manual serentak: SELAMAT tanpa lock tambahan
	// — UPDATE ni tulis baris `registration_payments`, gate kelulusan
	// (`setMemberStatus`) BACA baris tu (`HasSucceededRegistrationPayment`)
	// SEBELUM buka transaksi sendiri. Dua laluan sentuh baris BERBEZA
	// (registration_payments vs profiles), jadi tiada write-write
	// conflict yang perlukan kunci pesanan pola
	// `LockActivityForRegistration`. Kes paling teruk (webhook confirm
	// tiba SEPERSIS management approve): salah satu menang mengikut
	// turutan baca/tulis biasa Postgres (read committed) — either ahli
	// diluluskan (gate baca status 'succeeded' yang baru sahaja commit)
	// atau ditolak (gate baca sebelum commit, management cuba lagi
	// lepas bayaran disahkan). Tiada senario ahli diluluskan TANPA
	// bayaran 'succeeded' pernah wujud dalam DB.
	_, err = h.queries.UpdateRegistrationPaymentStatusByGatewayRef(c.Request.Context(), sqlc.UpdateRegistrationPaymentStatusByGatewayRefParams{
		Gateway:    h.gw.Name(),
		GatewayRef: event.GatewayRef,
		Status:     event.Status,
	})
	if err != nil {
		// pgx.ErrNoRows = tiada row layak dikemas kini: replay webhook
		// atas bayaran yang dah 'succeeded' (terminal), atau ref yang
		// bukan milik kita. Normal, bukan kegagalan. Ralat lain pun
		// sengaja tak digagalkan — ToyyibPay retry berterusan kalau
		// bukan 200.
		if !errors.Is(err, pgx.ErrNoRows) {
			log.Printf("update registration payment status (gateway=%s, ref=%s): %v", h.gw.Name(), event.GatewayRef, err)
		}
	}

	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ReturnPage — halaman landing selepas pembayar selesai di ToyyibPay
// (browser dibawa balik ke `returnURL` yang dihantar semasa `createBill`,
// BUKAN sumber kebenaran status). Sekadar makluman, padanan pola
// `AuthHandler.verificationHTMLPage` — pengesahan SEBENAR jalan async
// via Webhook (poll `getBillTransactions`), bukan di sini.
func (h *RegistrationPaymentHandler) ReturnPage(c *gin.Context) {
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(`<!doctype html>
<html lang="ms"><head><meta charset="utf-8"><title>Yuran Pendaftaran MARC</title></head>
<body style="font-family: sans-serif; padding: 40px; text-align: center;">
<h2>MARC</h2>
<p>Terima kasih. Pembayaran anda sedang diproses. Boleh kembali ke app MARC — status akan dikemas kini automatik sebaik pembayaran disahkan.</p>
</body></html>`))
}

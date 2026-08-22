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
	"marc/internal/paymentlog"
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
		paymentlog.Record(ctx, h.queries, paymentlog.Entry{
			Module: paymentlog.ModuleRegistrationFee, Event: paymentlog.EventCheckoutFailed,
			Status: paymentlog.StatusError, Gateway: h.gw.Name(), UserID: &userID,
			Message: "sudah diluluskan",
		})
		c.JSON(http.StatusBadRequest, gin.H{"error": "akaun anda sudah diluluskan, tiada yuran pendaftaran perlu dibayar"})
		return
	}

	alreadyPaid, err := h.queries.HasSucceededRegistrationPayment(ctx, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal mulakan pembayaran"})
		return
	}
	if alreadyPaid {
		paymentlog.Record(ctx, h.queries, paymentlog.Entry{
			Module: paymentlog.ModuleRegistrationFee, Event: paymentlog.EventCheckoutFailed,
			Status: paymentlog.StatusError, Gateway: h.gw.Name(), UserID: &userID,
			Message: "sudah dibayar",
		})
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
			paymentlog.Record(ctx, h.queries, paymentlog.Entry{
				Module: paymentlog.ModuleRegistrationFee, Event: paymentlog.EventCheckoutFailed,
				Status: paymentlog.StatusError, Gateway: h.gw.Name(), UserID: &userID,
				Message: "phone_required",
			})
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

	// ---- L29: BARIS DB DAHULU, BIL GATEWAY KEMUDIAN ----
	//
	// Susunan ni ialah keseluruhan pembaikan. Sebelum ni createBill
	// berjalan dahulu dan INSERT kemudian — jadi INSERT yang gagal
	// meninggalkan bil ToyyibPay yang SAH dan boleh dibayar tanpa
	// sebarang baris merujuknya. Kalau ahli bayar bil itu: webhook
	// mengena 0 baris dan menyenyapkannya sebagai "replay biasa", dan
	// reconcile melelar baris `registration_payments` jadi ia buta
	// kepada apa yang tak pernah wujud. Duit masuk, sifar rekod.
	//
	// Dibalikkan, kegagalan yang setara jadi tak berbahaya: yang tinggal
	// ialah baris 'pending' TANPA ref — kelihatan dalam sejarah bayaran
	// ahli, boleh diaudit, dan TIADA bil untuk sesiapa bayar.
	regPayment, err := h.queries.CreateRegistrationPayment(ctx, sqlc.CreateRegistrationPaymentParams{
		UserID:      userID,
		AmountCents: int32(h.feeCents),
		Currency:    "myr",
		Gateway:     h.gw.Name(),
	})
	if err != nil {
		log.Printf("create registration payment row: %v", err)
		paymentlog.Record(ctx, h.queries, paymentlog.Entry{
			Module:      paymentlog.ModuleRegistrationFee,
			Event:       paymentlog.EventCheckoutFailed,
			Status:      paymentlog.StatusError,
			Gateway:     h.gw.Name(),
			AmountCents: &h.feeCents,
			UserID:      &userID,
			Message:     truncateForLog("baris DB gagal ditulis sebelum createBill: " + err.Error()),
		})
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal mulakan pembayaran"})
		return
	}

	result, err := h.gw.CreatePayment(ctx, payment.CreateParams{
		AmountCents: h.feeCents,
		Currency:    "myr",
		Metadata:    metadata,
	})
	if err != nil {
		// Tiada bil wujud, jadi baris 'pending' tu takkan pernah
		// diselesaikan. Tandakan 'failed' supaya ia tak duduk selamanya
		// dalam sejarah ahli sebagai "sedang diproses" — dan supaya
		// `GetLatestRegistrationPaymentStatus` (yang mengisi skrin /me)
		// melaporkan sesuatu yang jujur.
		log.Printf("%s create payment (registration fee): %v", h.gw.Name(), err)
		if merr := h.queries.MarkRegistrationPaymentFailed(ctx, regPayment.ID); merr != nil {
			log.Printf("tanda registration payment %s sebagai failed: %v", regPayment.ID, merr)
		}
		paymentlog.Record(ctx, h.queries, paymentlog.Entry{
			Module:      paymentlog.ModuleRegistrationFee,
			Event:       paymentlog.EventCheckoutFailed,
			Status:      paymentlog.StatusError,
			Gateway:     h.gw.Name(),
			AmountCents: &h.feeCents,
			UserID:      &userID,
			RelatedID:   &regPayment.ID,
			Message:     truncateForLog(err.Error()),
		})
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal mulakan pembayaran"})
		return
	}

	if _, err := h.queries.SetRegistrationPaymentGatewayRef(ctx, sqlc.SetRegistrationPaymentGatewayRefParams{
		ID:         regPayment.ID,
		GatewayRef: pgtype.Text{String: result.GatewayRef, Valid: true},
	}); err != nil {
		// TETINGKAP BAKI — jauh lebih sempit drpd sebelum ni, tapi bukan
		// sifar. Bil wujud dan baris wujud; cuma pautan antara keduanya
		// yang hilang, jadi webhook (yang mencari ikut ref) takkan
		// menemuinya.
		//
		// Beza pentingnya: baris itu KINI WUJUD dan membawa user_id +
		// amaun + timestamp, jadi mendamaikannya secara manual ialah satu
		// UPDATE dan bukan siasatan forensik. Log ERROR + paymentlog
		// membawa kedua-dua belah pautan yang perlu disambung.
		log.Printf("ERROR registration_payment: bil %s DICIPTA di %s tapi gagal dipautkan ke baris %s (user=%s) — perlukan pautan manual: %v",
			result.GatewayRef, h.gw.Name(), regPayment.ID, userID, err)
		paymentlog.Record(ctx, h.queries, paymentlog.Entry{
			Module:      paymentlog.ModuleRegistrationFee,
			Event:       paymentlog.EventCheckoutFailed,
			Status:      paymentlog.StatusMismatch,
			Gateway:     h.gw.Name(),
			GatewayRef:  result.GatewayRef,
			AmountCents: &h.feeCents,
			UserID:      &userID,
			RelatedID:   &regPayment.ID,
			Message:     truncateForLog("bil dicipta tapi gagal dipautkan ke baris bayaran: " + err.Error()),
			RawPayload:  []byte(result.RawResponse),
		})
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal mulakan pembayaran"})
		return
	}

	paymentlog.Record(ctx, h.queries, paymentlog.Entry{
		Module:      paymentlog.ModuleRegistrationFee,
		Event:       paymentlog.EventCheckoutCreated,
		Status:      paymentlog.StatusOK,
		Gateway:     h.gw.Name(),
		GatewayRef:  result.GatewayRef,
		AmountCents: &h.feeCents,
		UserID:      &userID,
		RelatedID:   &regPayment.ID,
		RawPayload:  []byte(result.RawResponse),
	})

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

	// Rekod payload MENTAH dahulu, sebelum apa-apa parsing/pengesahan —
	// lihat komen padanan di DonationHandler.Webhook.
	paymentlog.Record(c.Request.Context(), h.queries, paymentlog.Entry{
		Module:     paymentlog.ModuleRegistrationFee,
		Event:      paymentlog.EventWebhookReceived,
		Status:     paymentlog.StatusOK,
		Gateway:    h.gw.Name(),
		RawPayload: payload,
	})

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
		paymentlog.Record(c.Request.Context(), h.queries, paymentlog.Entry{
			Module:  paymentlog.ModuleRegistrationFee,
			Event:   paymentlog.EventWebhookVerifyFailed,
			Status:  paymentlog.StatusError,
			Gateway: h.gw.Name(),
			Message: truncateForLog(err.Error()),
		})
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
	updated, err := h.queries.UpdateRegistrationPaymentStatusByGatewayRef(c.Request.Context(), sqlc.UpdateRegistrationPaymentStatusByGatewayRefParams{
		Gateway: h.gw.Name(),
		// `gateway_ref` nullable sejak L29. Baris yang belum berpaut
		// (ref NULL) tak boleh dipadankan oleh `=`, yang memang betul:
		// bil untuk baris itu tak pernah wujud, jadi tiada webhook sah
		// boleh merujuknya.
		GatewayRef: pgtype.Text{String: event.GatewayRef, Valid: true},
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
	} else {
		// `err == nil` = row BENAR-BENAR beralih (WHERE `status <>
		// 'succeeded'` terkena), bukan replay — padanan komen di
		// DonationHandler.Webhook.
		amountCents := int64(updated.AmountCents)
		paymentlog.Record(c.Request.Context(), h.queries, paymentlog.Entry{
			Module:      paymentlog.ModuleRegistrationFee,
			Event:       paymentlog.EventStatusUpdated,
			Status:      event.Status,
			Gateway:     h.gw.Name(),
			GatewayRef:  event.GatewayRef,
			AmountCents: &amountCents,
			RelatedID:   &updated.ID,
		})
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

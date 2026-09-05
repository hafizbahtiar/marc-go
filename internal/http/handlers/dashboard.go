// Package handlers - DashboardHandler menyediakan satu bacaan agregat
// untuk skrin Utama app (GET /dashboard). Payload berbentuk-role: blok
// `member` untuk semua pemanggil, blok `admin` hanya untuk rank >=
// "admin". Rasional penuh + kontrak:
// docs/superpowers/specs/2026-09-05-dashboard-home-design.md
package handlers

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"marc/internal/authz"
	"marc/internal/db/sqlc"
	"marc/internal/http/middleware"
)

type DashboardHandler struct {
	queries  *sqlc.Queries
	feeCents int64
}

// registrationFeeCents - jumlah (sen) yuran pendaftaran SEMASA
// (`REGISTRATION_FEE_CENTS`), sama pemalar yang ProfileHandler guna
// (profile.go) - satu-satunya nilai konfigurasi, bukan sesuatu yang
// diagak semula di sini.
func NewDashboardHandler(pool *pgxpool.Pool, registrationFeeCents int) *DashboardHandler {
	return &DashboardHandler{queries: sqlc.New(pool), feeCents: int64(registrationFeeCents)}
}

type membershipBlock struct {
	Status          string  `json:"status"`
	MemberID        *string `json:"member_id"`
	StaffIDVerified bool    `json:"staff_id_verified"`
	// OutstandingRegistrationFeeCents - null kalau ahli TAK terhutang
	// yuran pendaftaran, jumlah (sen) SEMASA kalau terhutang. "Terhutang
	// atau tidak" dikira oleh outstandingRegistrationFee (payments.go) -
	// fungsi SAMA yang /me/payments guna untuk medan
	// `outstanding_registration_fee` (boolean) - jangan tulis semula
	// logik itu di sini.
	//
	// Unit BERBEZA drpd /me/payments dengan sengaja: /me/payments cuma
	// perlu flag ya/tidak untuk papar CTA, /dashboard perlu jumlah SEBENAR
	// supaya kad boleh papar "RMxx.xx tertunggak" tanpa panggilan kedua -
	// sama corak dengan `RegistrationFeeCents` dalam profileResponse
	// (profile.go). nil vs angka adalah cara `outstanding_registration_fee`
	// (bool) diserikan semula di sini, bukan pengiraan berasingan.
	OutstandingRegistrationFeeCents *int64 `json:"outstanding_registration_fee_cents"`
}

// Kiraan notifikasi belum baca dan senarai "aktiviti saya" SENGAJA
// tiada di sini: skrin Utama tidak lagi memaparkan kedua-duanya
// (notifikasi ialah tab bottom-nav dengan lencananya sendiri, dan
// "aktiviti saya" mencerminkan tab Aktiviti / `/my-activities`).
// Endpoint ini berhenti mengiranya sekali - `upcoming_registrations`
// membawa satu subquery per baris, jadi ia bukan sekadar medan mati
// dalam JSON tetapi kerja pangkalan data yang tiada sesiapa baca.
type memberBlock struct {
	Membership        membershipBlock    `json:"membership"`
	CertificatesTotal int64              `json:"certificates_total"`
	TotalMembers      int64              `json:"total_members"`
	OpenActivities    []openActivityItem `json:"open_activities"`
}

type openActivityItem struct {
	ID                uuid.UUID `json:"id"`
	Title             string    `json:"title"`
	StartsAt          time.Time `json:"starts_at"`
	CategoryName      string    `json:"category_name"`
	FeeCents          int32     `json:"fee_cents"`
	Currency          string    `json:"currency"`
	RegistrationCount int64     `json:"registration_count"`
}

type dashboardResponse struct {
	Member memberBlock `json:"member"`
	Admin  *adminBlock `json:"admin"`
}

type departmentStat struct {
	Code  string `json:"code"`
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

type memberStats struct {
	Active       int64            `json:"active"`
	Pending      int64            `json:"pending"`
	NewThisMonth int64            `json:"new_this_month"`
	ByDepartment []departmentStat `json:"by_department"`
}

type activityStats struct {
	Upcoming               int64    `json:"upcoming"`
	RegistrationsThisMonth int64    `json:"registrations_this_month"`
	AttendanceRate         *float64 `json:"attendance_rate"`
}

// adminBlock diisi dalam Task 3; Task 4 menambah RevenueThisMonth pada
// struct yang sama. Hanya pemanggil rank >= adminRoleKey menerimanya -
// selainnya, medan `admin` menyiri sebagai null (lihat Get).
type adminBlock struct {
	PendingApprovals int64         `json:"pending_approvals"`
	MemberStats      memberStats   `json:"member_stats"`
	ActivityStats    activityStats `json:"activity_stats"`
	RevenueThisMonth revenueBlock  `json:"revenue_this_month"`
}

type revenueBlock struct {
	Currency          string `json:"currency"`
	RegistrationCents int64  `json:"registration_cents"`
	ActivityCents     int64  `json:"activity_cents"`
	// Nil untuk admin bukan-superadmin: DATABASE.md mengunci data derma
	// kepada superadmin dalam /admin/payments, jadi agregat yang
	// mencampurnya akan menyelinapkan angka itu kepada admin melalui
	// pintu belakang. TotalCents hanya menjumlahkan apa yang pemanggil
	// layak lihat.
	DonationCents *int64 `json:"donation_cents"`
	TotalCents    int64  `json:"total_cents"`
}

// revenueCurrency - satu mata wang di seluruh sistem buat masa ini.
const revenueCurrency = "MYR"

// maxDepartmentRows - kad memuatkan segelintir baris; selebihnya
// digabung sebagai "Lain-lain" supaya jumlah kekal tepat tanpa
// menghantar 40 baris untuk sebuah carta kecil.
const maxDepartmentRows = 6

// adminRoleKey - siling blok statistik dashboard. Padanan
// superAdminRoleKey dalam payments.go; rank sebenar ditentukan oleh
// jadual `roles`, bukan pemalar ini.
const adminRoleKey = "admin"

// attendanceRateToFloat - AttendanceRate disiri sqlc sebagai interface{}
// (bukan pgtype.Float8 seperti dijangka brief) kerana ia hasil ungkapan
// CASE bersarang dalam sub-query berkorelaso, bukan lajur/pgtype yang
// dikenali sqlc secara statik. Jenis dinamik sebenar bergantung pada
// cara pgx memetakan hasil itu (boleh jadi float64, *float64,
// pgtype.Float8, pgtype.Numeric, atau nil terus bila NULL) - jadi
// assertion mesti bentuk selamat (comma-ok) untuk SETIAP bentuk yang
// mungkin, bukan assertion telanjang yang panic bila jenis meleset.
// Sebarang bentuk yang tidak dikenali jatuh balik kepada nil (JSON
// null), BUKAN 0 - 0 boleh disalahtafsir sebagai kadar kehadiran 0%.
func attendanceRateToFloat(raw any) *float64 {
	switch v := raw.(type) {
	case nil:
		return nil
	case float64:
		return &v
	case float32:
		f := float64(v)
		return &f
	case *float64:
		return v
	case pgtype.Float8:
		if !v.Valid {
			return nil
		}
		f := v.Float64
		return &f
	case pgtype.Numeric:
		if !v.Valid {
			return nil
		}
		f, err := v.Float64Value()
		if err != nil || !f.Valid {
			return nil
		}
		return &f.Float64
	default:
		return nil
	}
}

func (h *DashboardHandler) buildAdminBlock(ctx context.Context, userID uuid.UUID) (*adminBlock, error) {
	pending, err := h.queries.CountPendingMembers(ctx)
	if err != nil {
		return nil, err
	}
	active, err := h.queries.CountApprovedMembers(ctx)
	if err != nil {
		return nil, err
	}
	baharu, err := h.queries.CountNewMembersThisMonth(ctx)
	if err != nil {
		return nil, err
	}
	deptRows, err := h.queries.MemberStatsByDepartment(ctx)
	if err != nil {
		return nil, err
	}
	act, err := h.queries.ActivityStatsThisMonth(ctx)
	if err != nil {
		return nil, err
	}

	depts := make([]departmentStat, 0, maxDepartmentRows+1)
	var lain int64
	for i, r := range deptRows {
		if i < maxDepartmentRows {
			depts = append(depts, departmentStat{Code: r.Code, Name: r.Name, Count: r.Count})
			continue
		}
		lain += r.Count
	}
	if lain > 0 {
		depts = append(depts, departmentStat{Code: "", Name: "Lain-lain", Count: lain})
	}

	kadar := attendanceRateToFloat(act.AttendanceRate)

	reg, err := h.queries.SumRegistrationRevenueThisMonth(ctx)
	if err != nil {
		return nil, err
	}
	aktiviti, err := h.queries.SumActivityRevenueThisMonth(ctx)
	if err != nil {
		return nil, err
	}

	revenue := revenueBlock{
		Currency:          revenueCurrency,
		RegistrationCents: reg,
		ActivityCents:     aktiviti,
		TotalCents:        reg + aktiviti,
	}

	// Derma hanya dikira & didedahkan kepada superadmin - lihat komen
	// pada DonationCents di atas.
	isSuperAdmin, err := authz.IsAtLeastRole(ctx, h.queries, userID, superAdminRoleKey)
	if err != nil {
		return nil, err
	}
	if isSuperAdmin {
		derma, err := h.queries.SumDonationRevenueThisMonth(ctx)
		if err != nil {
			return nil, err
		}
		revenue.DonationCents = &derma
		revenue.TotalCents += derma
	}

	return &adminBlock{
		PendingApprovals: pending,
		MemberStats: memberStats{
			Active:       active,
			Pending:      pending,
			NewThisMonth: baharu,
			ByDepartment: depts,
		},
		ActivityStats: activityStats{
			Upcoming:               act.Upcoming,
			RegistrationsThisMonth: act.RegistrationsThisMonth,
			AttendanceRate:         kadar,
		},
		RevenueThisMonth: revenue,
	}, nil
}

// latestPendingRegistrationFeeCents - jumlah (sen) yang patut dipaparkan
// utk yuran pendaftaran TERTUNGGAK: jumlah baris 'pending' TERBARU
// (`regRows`, sudah tersusun `created_at desc` - lihat
// ListMyRegistrationPayments), BUKAN `configFeeCents` (fi SEMASA dari
// REGISTRATION_FEE_CENTS).
//
// Sebab: `CreateRegistrationPayment` (registration_payment.go) rekod
// `amount_cents` PADA MASA bil dicipta (snapshot), sama corak yang
// `FeeCentsPaid` guna utk resit yuran aktiviti
// (activity_registration_payment.go, "snapshot amaun SEBENAR dihantar
// ke gateway ... resit baca lajur ni supaya yuran yang ditukar SELEPAS
// bayar tak senyap ubah resit sedia wujud"). Bil ToyyibPay ahli yang
// SEDANG menunggu bayaran caj jumlah SNAPSHOT itu, bukan konfigurasi
// semasa - kalau fi berubah (RM10 -> RM20) semasa ahli memegang baris
// 'pending' lama, dashboard MESTI papar jumlah bil sebenar yang akan
// dicaj, bukan angka baharu yang tak dicaj kepadanya.
//
// Fallback kepada `configFeeCents` HANYA bila tiada baris 'pending'
// langsung - ahli belum pernah cuba bayar, jadi konfigurasi semasa
// memang-lah jumlah yang akan dicaj bila dia mula bayar nanti.
func latestPendingRegistrationFeeCents(regRows []sqlc.RegistrationPayment, configFeeCents int64) int64 {
	for _, r := range regRows {
		if r.Status == "pending" {
			return int64(r.AmountCents)
		}
	}
	return configFeeCents
}

// Get - GET /dashboard. Didaftar dalam kumpulan `approved`, jadi
// RequireApprovedStatus sudah menolak ahli pending/rejected sebelum
// sampai sini.
func (h *DashboardHandler) Get(c *gin.Context) {
	ctx := c.Request.Context()
	userID := middleware.UserID(c)

	profile, err := h.queries.GetProfileByUserID(ctx, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal muat dashboard"})
		return
	}
	certs, err := h.queries.CountMyCertificates(ctx, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal muat dashboard"})
		return
	}
	totalMembers, err := h.queries.CountApprovedMembers(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal muat dashboard"})
		return
	}
	openRows, err := h.queries.ListOpenActivitiesForMe(ctx, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal muat dashboard"})
		return
	}
	regPayments, err := h.queries.ListMyRegistrationPayments(ctx, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal muat dashboard"})
		return
	}

	// make(..., 0, n) BUKAN var nil - slice nil menyiri sebagai `null`,
	// dan client Flutter mengharapkan array (senarai kosong = tiada
	// aktiviti, bukan medan hilang).
	open := make([]openActivityItem, 0, len(openRows))
	for _, r := range openRows {
		open = append(open, openActivityItem{
			ID:                r.ID,
			Title:             r.Title,
			StartsAt:          r.StartsAt.Time,
			CategoryName:      r.CategoryName,
			FeeCents:          r.FeeCents,
			Currency:          r.Currency,
			RegistrationCount: r.RegistrationCount,
		})
	}

	isAdmin, err := authz.IsAtLeastRole(ctx, h.queries, userID, adminRoleKey)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal muat dashboard"})
		return
	}
	var admin *adminBlock
	if isAdmin {
		admin, err = h.buildAdminBlock(ctx, userID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal muat dashboard"})
			return
		}
	}

	// outstandingRegistrationFee (payments.go) - SAMA fungsi yang
	// /me/payments guna, lihat komen OutstandingRegistrationFeeCents di
	// atas. Fungsi ni HANYA jawab soalan "terhutang atau tidak" - jumlah
	// (di bawah) sengaja dikira BERASINGAN supaya "owes?" kekal SATU
	// tempat, bukan digandingkan dengan "berapa?".
	var outstandingFeeCents *int64
	if outstandingRegistrationFee(profile, regPayments) {
		fee := latestPendingRegistrationFeeCents(regPayments, h.feeCents)
		outstandingFeeCents = &fee
	}

	c.JSON(http.StatusOK, dashboardResponse{
		Member: memberBlock{
			Membership: membershipBlock{
				Status:                          profile.Status,
				MemberID:                        textToPtr(profile.MemberID),
				StaffIDVerified:                 profile.StaffIDVerifiedAt.Valid,
				OutstandingRegistrationFeeCents: outstandingFeeCents,
			},
			CertificatesTotal: certs,
			TotalMembers:      totalMembers,
			OpenActivities:    open,
		},
		Admin: admin,
	})
}

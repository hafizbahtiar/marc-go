# Dashboard sebagai Home - Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Jadikan dashboard role-aware sebagai halaman utama app, dengan feed post berpindah ke tab kedua.

**Architecture:** Satu endpoint baharu `GET /dashboard` dalam marc_go memulangkan payload berbentuk-role (`member` sentiasa, `admin` hanya untuk rank >= 80). marc_flutter menambah modul `features/dashboard/` dengan satu `FutureProvider`, dan bottom nav berkembang daripada 4 ke 5 tab dengan dashboard di indeks 0. Gate ahli pending/emel-belum-sah berpindah daripada FeedPage ke DashboardPage kerana dashboard kini destinasi lalai selepas log masuk.

**Tech Stack:** Go 1.x + gin + pgx/v5 + sqlc + goose (marc_go); Flutter + Riverpod + go_router + dio (marc_flutter); Postgres.

**Spec:** `docs/superpowers/specs/2026-09-05-dashboard-home-design.md` (dalam repo marc_go)

## Global Constraints

- **Dua repo.** `marc_go` di `/Users/hafiz/Developments/marc_go`, `marc_flutter` di `/Users/hafiz/Developments/marc_flutter`. Setiap task menyatakan repo mana. Jangan silang.
- **JANGAN commit tanpa kebenaran eksplisit pemilik repo.** Arahan pemilik pada 2026-09-05: stage sahaja. Langkah "Commit" dalam setiap task bermaksud jalankan `git add` bagi fail yang disenaraikan, kemudian **tanya** sebelum `git commit`.
- Branch semasa marc_go: `staging`.
- Bahasa komen kod, mesej ralat API, dan teks UI: **Bahasa Melayu**, konsisten dengan kod sedia ada.
- Nama medan JSON: **snake_case**.
- Ambang role blok admin: `admin` (rank 80) ke atas. Guna `authz.IsAtLeastRole(ctx, q, userID, "admin")`, jangan bandingkan rank secara manual.
- Derma: `donation_cents` diisi **hanya** untuk `superadmin`; `null` untuk admin, dan `total_cents` tidak termasuk derma dalam kes itu.
- Amaun bayaran sentiasa dibaca daripada snapshot pada baris bayaran (`registration_payments.amount_cents`, `activity_registrations.fee_cents_paid`, `donations.amount_cents`) — **jangan** baca `activities.fee_cents` hidup.
- "Bulan semasa" = bulan kalendar `now()` pada server. Tiada parameter julat tarikh.
- Selepas mengedit `queries/*.sql`, jalankan `sqlc generate` sebelum kod Go yang menggunakannya boleh dikompil.
- Ujian live Go dilangkau kecuali `HANDLER_TEST_DB` (atau `ACTIVITY_TEST_DB`) ditetapkan. Tetapkannya sebelum menjalankan ujian task Go, kalau tidak "PASS" bermakna "dilangkau".

---

## Struktur fail

**marc_go — cipta:**

| Fail | Tanggungjawab |
|---|---|
| `queries/dashboard.sql` | semua query khusus dashboard (10 query) |
| `internal/http/handlers/dashboard.go` | handler + jenis respons JSON |
| `internal/http/handlers/dashboard_live_test.go` | ujian live end-to-end handler |

**marc_go — ubah:**

| Fail | Perubahan |
|---|---|
| `internal/http/router.go` | daftar `approved.GET("/dashboard", …)` |
| `TODO.md` | catat ciri baharu |

**marc_flutter — cipta:**

| Fail | Tanggungjawab |
|---|---|
| `lib/features/dashboard/dashboard_models.dart` | model + `fromJson` defensif |
| `lib/features/dashboard/dashboard_providers.dart` | `dashboardProvider` |
| `lib/features/dashboard/dashboard_page.dart` | susun atur skrin + gate |
| `lib/features/dashboard/widgets/member_cards.dart` | kad ahli |
| `lib/features/dashboard/widgets/admin_section.dart` | blok admin |
| `lib/shared/ui/widgets/email_not_verified_view.dart` | diekstrak drpd `feed_page.dart` |
| `test/features/dashboard/dashboard_models_test.dart` | ujian penghuraian |
| `test/features/dashboard/dashboard_page_test.dart` | ujian widget |
| `test/app/nav_shell_test.dart` | ujian pengikat nav/router |

**marc_flutter — ubah:**

| Fail | Perubahan |
|---|---|
| `lib/app/nav_shell.dart` | 4 → 5 destinasi |
| `lib/app/router.dart` | branch `/dashboard` di indeks 0; dua rujukan `/feed` |
| `lib/shared/ui/widgets/approval_gate.dart:120` | `/feed` → `/dashboard` |
| `lib/features/posts/feed_page.dart` | guna `EmailNotVerifiedView` kongsi |

---

# BAHAGIAN A — marc_go

### Task 1: Endpoint `/dashboard` dengan blok `member` asas

Deliverable: endpoint hidup yang memulangkan tiga kiraan + status keahlian, dan menolak ahli pending.

**Files:**
- Create: `marc_go/queries/dashboard.sql`
- Create: `marc_go/internal/http/handlers/dashboard.go`
- Create: `marc_go/internal/http/handlers/dashboard_live_test.go`
- Modify: `marc_go/internal/http/router.go` (kumpulan `approved`, berhampiran baris 187-208)

**Interfaces:**
- Consumes: `middleware.UserID(c) uuid.UUID`, `sqlc.New(pool) *sqlc.Queries`, helper ujian sedia ada `activityTestPool(t)` (`activities_live_test.go:39`) dan `seedMember(t, ctx, pool, roleKey, status)` (`profile_status_live_test.go:50`).
- Produces:
  - `func NewDashboardHandler(pool *pgxpool.Pool) *DashboardHandler`
  - `func (h *DashboardHandler) Get(c *gin.Context)`
  - jenis `dashboardResponse{ Member memberBlock `json:"member"`; Admin *adminBlock `json:"admin"` }`
  - `memberBlock` dengan medan `UnreadNotifications`, `CertificatesTotal`, `TotalMembers`, `Membership` (Task 2 dan 5 menambah medan pada struct yang SAMA).

- [ ] **Step 1: Tulis query**

Cipta `queries/dashboard.sql`:

```sql
-- name: CountUnreadNotifications :one
select count(*) from notifications
where recipient_id = $1 and read_at is null;

-- name: CountMyCertificates :one
select count(*) from activity_certificates
where user_id = $1 and revoked_at is null;

-- name: CountApprovedMembers :one
select count(*) from profiles
where status = 'approved' and active = true;
```

Semak dahulu nama lajur sebenar: `psql "$HANDLER_TEST_DB" -c '\d activity_certificates'` dan `\d profiles`. Kalau `activity_certificates` tiada `revoked_at` (query `RevokeCertificate` wujud dalam `queries/activity_certificates.sql`, jadi ia sepatutnya ada), padankan dengan lajur sebenar dan bukan tekaan. Kalau `profiles` tiada lajur `active`, gugurkan syarat itu.

- [ ] **Step 2: Jana kod sqlc**

Run: `cd /Users/hafiz/Developments/marc_go && sqlc generate`
Expected: tiada ralat; `internal/db/sqlc/dashboard.sql.go` muncul dengan `CountUnreadNotifications`, `CountMyCertificates`, `CountApprovedMembers`.

- [ ] **Step 3: Tulis ujian yang gagal**

Cipta `internal/http/handlers/dashboard_live_test.go`:

```go
package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// callDashboard panggil handler terus (corak minePayments dalam
// my_payments_live_test.go) - tiada middleware, jadi ujian 403 di bawah
// menguji gate handler, BUKAN RequireApprovedStatus.
func callDashboard(t *testing.T, pool *pgxpool.Pool, userID uuid.UUID) (int, map[string]any) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	c.Set("userID", userID)

	NewDashboardHandler(pool).Get(c)

	var body map[string]any
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("nyahsiri respons: %v (badan: %s)", err, rec.Body.String())
		}
	}
	return rec.Code, body
}

func TestDashboardAhliBiasaDapatBlokMemberTanpaAdmin(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	userID := seedMember(t, ctx, pool, "ahli", "approved")

	code, body := callDashboard(t, pool, userID)
	if code != http.StatusOK {
		t.Fatalf("kod = %d, mahu 200. Badan: %v", code, body)
	}
	if body["admin"] != nil {
		t.Errorf("admin = %v, mahu null untuk ahli biasa", body["admin"])
	}
	member, ok := body["member"].(map[string]any)
	if !ok {
		t.Fatalf("member bukan objek: %v", body["member"])
	}
	for _, key := range []string{"unread_notifications", "certificates_total", "total_members"} {
		if _, ada := member[key]; !ada {
			t.Errorf("member tiada medan %q", key)
		}
	}
	membership, ok := member["membership"].(map[string]any)
	if !ok {
		t.Fatalf("membership bukan objek: %v", member["membership"])
	}
	if membership["status"] != "approved" {
		t.Errorf("membership.status = %v, mahu approved", membership["status"])
	}
}

func TestDashboardTotalMembersKiraAhliApproved(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	userID := seedMember(t, ctx, pool, "ahli", "approved")

	_, sebelum := callDashboard(t, pool, userID)
	asal := sebelum["member"].(map[string]any)["total_members"].(float64)

	seedMember(t, ctx, pool, "ahli", "approved")
	seedMember(t, ctx, pool, "ahli", "pending") // TIDAK dikira

	_, selepas := callDashboard(t, pool, userID)
	kini := selepas["member"].(map[string]any)["total_members"].(float64)

	if kini != asal+1 {
		t.Errorf("total_members = %v, mahu %v (pending tidak sepatutnya dikira)", kini, asal+1)
	}
}
```

- [ ] **Step 4: Jalankan ujian, sahkan ia GAGAL**

Run:
```bash
cd /Users/hafiz/Developments/marc_go
export HANDLER_TEST_DB="postgres://…"   # DSN DB ujian tempatan
go test ./internal/http/handlers/ -run TestDashboard -v
```
Expected: GAGAL kompil — `undefined: NewDashboardHandler`. Kalau outputnya `SKIP`, `HANDLER_TEST_DB` tidak ditetapkan — betulkan sebelum meneruskan, ujian yang dilangkau tidak membuktikan apa-apa.

- [ ] **Step 5: Tulis handler minimum**

Cipta `internal/http/handlers/dashboard.go`:

```go
// Package handlers - DashboardHandler menyediakan satu bacaan agregat
// untuk skrin Utama app (GET /dashboard). Payload berbentuk-role: blok
// `member` untuk semua pemanggil, blok `admin` hanya untuk rank >=
// "admin". Rasional penuh + kontrak:
// docs/superpowers/specs/2026-09-05-dashboard-home-design.md
package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	"marc/internal/db/sqlc"
	"marc/internal/http/middleware"
)

type DashboardHandler struct {
	queries *sqlc.Queries
}

func NewDashboardHandler(pool *pgxpool.Pool) *DashboardHandler {
	return &DashboardHandler{queries: sqlc.New(pool)}
}

type membershipBlock struct {
	Status           string  `json:"status"`
	MemberID         *string `json:"member_id"`
	StaffIDVerified  bool    `json:"staff_id_verified"`
}

type memberBlock struct {
	Membership          membershipBlock `json:"membership"`
	UnreadNotifications int64           `json:"unread_notifications"`
	CertificatesTotal   int64           `json:"certificates_total"`
	TotalMembers        int64           `json:"total_members"`
}

type dashboardResponse struct {
	Member memberBlock `json:"member"`
	Admin  *adminBlock `json:"admin"`
}

// adminBlock diisi dalam Task 3-4; kekal kosong buat masa ini supaya
// medan `admin` menyiri sebagai null dan bukan hilang terus.
type adminBlock struct{}

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
	unread, err := h.queries.CountUnreadNotifications(ctx, userID)
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

	c.JSON(http.StatusOK, dashboardResponse{
		Member: memberBlock{
			Membership: membershipBlock{
				Status:          profile.Status,
				MemberID:        textToPtr(profile.MemberID),
				StaffIDVerified: profile.StaffIDVerifiedAt.Valid,
			},
			UnreadNotifications: unread,
			CertificatesTotal:   certs,
			TotalMembers:        totalMembers,
		},
		Admin: nil,
	})
}
```

Padankan jenis medan dengan struct sqlc sebenar: `profile.MemberID` mungkin `pgtype.Text` (guna `textToPtr`, `profile.go:2223`) atau `*string`. Baca `internal/db/sqlc/profiles.sql.go` dan padankan; jangan andaikan.

- [ ] **Step 6: Jalankan ujian, sahkan ia LULUS**

Run: `go test ./internal/http/handlers/ -run TestDashboard -v`
Expected: PASS untuk kedua-dua ujian.

- [ ] **Step 7: Daftar route**

Dalam `internal/http/router.go`, dalam kumpulan `approved` (berhampiran `approved.GET("/roles", …)`, baris ~257):

```go
	// Bacaan agregat skrin Utama app - kumpulan `approved` (bukan
	// `verified`): mengira notifikasi sendiri tidak mendedahkan
	// kandungan, dan client memapar skrin "sahkan emel" sebelum sempat
	// memanggil endpoint ini.
	approved.GET("/dashboard", handlers.NewDashboardHandler(pool).Get)
```

- [ ] **Step 8: Ujian gate — ahli pending kena 403**

`callDashboard` memanggil handler TERUS, jadi ia memintas middleware dan
tidak boleh membuktikan apa-apa tentang 403. Gate itu datang daripada
kumpulan route, jadi ujiannya mesti melalui router:

```go
func TestDashboardAhliPendingDitolak(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	pendingID := seedMember(t, ctx, pool, "ahli", "pending")

	gin.SetMode(gin.TestMode)
	r := gin.New()
	// Tiru rantaian kumpulan `approved` dalam router.go - RequireAuth
	// digantikan dengan suntikan userID terus supaya ujian ini menguji
	// SATU perkara sahaja: gate status.
	g := r.Group("/", func(c *gin.Context) { c.Set("userID", pendingID) },
		middleware.RequireApprovedStatus(sqlc.New(pool)))
	g.GET("/dashboard", NewDashboardHandler(pool).Get)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard", nil))

	if rec.Code != http.StatusForbidden {
		t.Errorf("kod = %d, mahu 403. Badan: %s", rec.Code, rec.Body.String())
	}
}
```

Tambah import `"marc/internal/http/middleware"` dan `"marc/internal/db/sqlc"`
pada fail ujian. Sahkan tandatangan sebenar `middleware.RequireApprovedStatus`
(`router.go:186` menunjukkan ia mengambil `sqlc.New(pool)`) dan nama kunci
context yang dibaca `middleware.UserID` — kalau bukan `"userID"`, padankan.

Run: `go test ./internal/http/handlers/ -run TestDashboardAhliPendingDitolak -v`
Expected: PASS.

- [ ] **Step 9: Sahkan binaan penuh**

Run: `go build ./... && go vet ./...`
Expected: tiada output (jaya).

- [ ] **Step 10: Stage**

```bash
git add queries/dashboard.sql internal/db/sqlc/ \
        internal/http/handlers/dashboard.go \
        internal/http/handlers/dashboard_live_test.go \
        internal/http/router.go
```
Kemudian **tanya pemilik repo** sebelum commit (lihat Global Constraints).

---

### Task 2: Senarai aktiviti dalam blok `member`

Deliverable: dashboard memaparkan sehingga 3 aktiviti yang ahli sudah daftar dan 3 yang dia belum daftar.

**Files:**
- Modify: `marc_go/queries/dashboard.sql`
- Modify: `marc_go/internal/http/handlers/dashboard.go`
- Modify: `marc_go/internal/http/handlers/dashboard_live_test.go`

**Interfaces:**
- Consumes: `memberBlock` daripada Task 1; helper ujian `seedActivity(t, pool)` (`activities_live_test.go:63`).
- Produces: medan `UpcomingRegistrations []upcomingRegistrationItem` dan `OpenActivities []openActivityItem` pada `memberBlock`.

- [ ] **Step 1: Tulis ujian yang gagal**

Tambah pada `dashboard_live_test.go`:

```go
func TestDashboardOpenActivitiesKecualikanYangSudahDidaftar(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	userID := seedMember(t, ctx, pool, "ahli", "approved")
	activityID := seedActivity(t, pool)

	// Terbitkan supaya ia layak muncul dalam open_activities.
	if _, err := pool.Exec(ctx,
		`update activities set status = 'published' where id = $1`, activityID); err != nil {
		t.Fatalf("terbitkan aktiviti: %v", err)
	}

	_, sebelum := callDashboard(t, pool, userID)
	if !mengandungiAktiviti(sebelum, "open_activities", activityID) {
		t.Fatalf("open_activities sepatutnya mengandungi aktiviti yang belum didaftar")
	}

	if _, err := pool.Exec(ctx,
		`insert into activity_registrations (activity_id, user_id, status)
		 values ($1, $2, 'registered')`, activityID, userID); err != nil {
		t.Fatalf("daftar: %v", err)
	}

	_, selepas := callDashboard(t, pool, userID)
	if mengandungiAktiviti(selepas, "open_activities", activityID) {
		t.Errorf("open_activities masih mengandungi aktiviti yang sudah didaftar")
	}
	if !mengandungiAktiviti(selepas, "upcoming_registrations", activityID) {
		t.Errorf("upcoming_registrations sepatutnya mengandungi pendaftaran baharu")
	}
}

// mengandungiAktiviti - kedua-dua senarai membawa id aktiviti, tetapi di
// bawah kunci berbeza: open_activities guna `id`, upcoming_registrations
// guna `activity_id` (`id` di sana ialah id PENDAFTARAN).
func mengandungiAktiviti(body map[string]any, senarai string, activityID uuid.UUID) bool {
	member, ok := body["member"].(map[string]any)
	if !ok {
		return false
	}
	items, ok := member[senarai].([]any)
	if !ok {
		return false
	}
	kunci := "id"
	if senarai == "upcoming_registrations" {
		kunci = "activity_id"
	}
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if ok && item[kunci] == activityID.String() {
			return true
		}
	}
	return false
}
```

Semak lajur wajib `activity_registrations` dahulu (`\d activity_registrations`) — kalau ada lajur NOT NULL tanpa default (cth `checkin_token`), tambah pada INSERT ujian, atau guna `seedRegistration` kalau helper begitu sudah wujud dalam pakej ujian.

- [ ] **Step 2: Jalankan ujian, sahkan ia GAGAL**

Run: `go test ./internal/http/handlers/ -run TestDashboardOpenActivities -v`
Expected: GAGAL — `open_activities sepatutnya mengandungi aktiviti yang belum didaftar` (medan belum wujud).

- [ ] **Step 3: Tambah query**

Tambah pada `queries/dashboard.sql`:

```sql
-- name: ListMyUpcomingRegistrations :many
-- Corak ListMyRegistrations (activity_registrations.sql:206) tetapi
-- hanya yang BELUM tamat dan dihadkan 3 - kad dashboard, bukan senarai
-- penuh (itu /my-activities).
select r.id, r.activity_id, r.payment_status,
  a.title, a.starts_at, a.ends_at, c.name as category_name
from activity_registrations r
join activities a on a.id = r.activity_id
join activity_categories c on c.id = a.category_id
where r.user_id = $1
  and r.status <> 'cancelled'
  and a.deleted_at is null
  and a.ends_at >= now()
order by a.starts_at asc
limit 3;

-- name: ListOpenActivitiesForMe :many
-- Aktiviti terbitan akan datang yang pemanggil BELUM daftar. `not
-- exists` (bukan left join + is null) supaya perancang boleh berhenti
-- pada padanan pertama.
select a.id, a.title, a.starts_at, a.fee_cents, a.currency,
  c.name as category_name,
  (select count(*) from activity_registrations r2
    where r2.activity_id = a.id and r2.status <> 'cancelled') as registration_count
from activities a
join activity_categories c on c.id = a.category_id
where a.deleted_at is null
  and a.status = 'published'
  and a.ends_at >= now()
  and not exists (
    select 1 from activity_registrations r
    where r.activity_id = a.id and r.user_id = $1 and r.status <> 'cancelled'
  )
order by a.starts_at asc
limit 3;
```

- [ ] **Step 4: Jana kod sqlc**

Run: `sqlc generate`
Expected: `ListMyUpcomingRegistrations` dan `ListOpenActivitiesForMe` muncul dalam `internal/db/sqlc/dashboard.sql.go`.

- [ ] **Step 5: Tambah pada handler**

Dalam `dashboard.go`, tambah jenis dan isikan medan:

```go
type upcomingRegistrationItem struct {
	ID            uuid.UUID `json:"id"`
	ActivityID    uuid.UUID `json:"activity_id"`
	Title         string    `json:"title"`
	StartsAt      time.Time `json:"starts_at"`
	EndsAt        time.Time `json:"ends_at"`
	CategoryName  string    `json:"category_name"`
	PaymentStatus string    `json:"payment_status"`
}

type openActivityItem struct {
	ID                uuid.UUID `json:"id"`
	Title             string    `json:"title"`
	StartsAt          time.Time `json:"starts_at"`
	CategoryName      string    `json:"category_name"`
	FeeCents          int64     `json:"fee_cents"`
	Currency          string    `json:"currency"`
	RegistrationCount int64     `json:"registration_count"`
}
```

Pada `memberBlock` tambah:

```go
	UpcomingRegistrations []upcomingRegistrationItem `json:"upcoming_registrations"`
	OpenActivities        []openActivityItem         `json:"open_activities"`
```

Dalam `Get`, selepas kiraan sedia ada:

```go
	regRows, err := h.queries.ListMyUpcomingRegistrations(ctx, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal muat dashboard"})
		return
	}
	openRows, err := h.queries.ListOpenActivitiesForMe(ctx, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal muat dashboard"})
		return
	}

	// make(..., 0, n) BUKAN var nil - slice nil menyiri sebagai `null`,
	// dan client Flutter mengharapkan array (senarai kosong = tiada
	// aktiviti, bukan medan hilang).
	upcoming := make([]upcomingRegistrationItem, 0, len(regRows))
	for _, r := range regRows {
		upcoming = append(upcoming, upcomingRegistrationItem{
			ID:            r.ID,
			ActivityID:    r.ActivityID,
			Title:         r.Title,
			StartsAt:      r.StartsAt,
			EndsAt:        r.EndsAt,
			CategoryName:  r.CategoryName,
			PaymentStatus: r.PaymentStatus,
		})
	}
	open := make([]openActivityItem, 0, len(openRows))
	for _, r := range openRows {
		open = append(open, openActivityItem{
			ID:                r.ID,
			Title:             r.Title,
			StartsAt:          r.StartsAt,
			CategoryName:      r.CategoryName,
			FeeCents:          r.FeeCents,
			Currency:          r.Currency,
			RegistrationCount: r.RegistrationCount,
		})
	}
```

dan hantar kedua-duanya dalam `memberBlock`. Padankan jenis dengan struct sqlc yang dijana (`StartsAt` mungkin `pgtype.Timestamptz` — kalau ya, tukar medan JSON kepada `time.Time` dengan `.Time` atau ikut corak penukaran yang digunakan `activities.go`).

- [ ] **Step 6: Jalankan ujian, sahkan ia LULUS**

Run: `go test ./internal/http/handlers/ -run TestDashboard -v`
Expected: PASS untuk ketiga-tiga ujian.

- [ ] **Step 7: Stage**

```bash
git add queries/dashboard.sql internal/db/sqlc/ \
        internal/http/handlers/dashboard.go \
        internal/http/handlers/dashboard_live_test.go
```

---

### Task 3: Blok `admin` — gate role + pending, statistik ahli & aktiviti

Deliverable: pemanggil rank >= 80 menerima statistik organisasi; semua yang lain terus menerima `admin: null`.

**Files:**
- Modify: `marc_go/queries/dashboard.sql`
- Modify: `marc_go/internal/http/handlers/dashboard.go`
- Modify: `marc_go/internal/http/handlers/dashboard_live_test.go`

**Interfaces:**
- Consumes: `authz.IsAtLeastRole(ctx, *sqlc.Queries, uuid.UUID, string) (bool, error)`; `adminBlock` kosong daripada Task 1.
- Produces: `adminBlock` dengan `PendingApprovals int64`, `MemberStats memberStats`, `ActivityStats activityStats`. Task 4 menambah `RevenueThisMonth` pada struct yang SAMA.

- [ ] **Step 1: Tulis ujian yang gagal**

```go
func TestDashboardBlokAdminIkutRole(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	kes := []struct {
		roleKey  string
		mahuBlok bool
	}{
		{"ahli", false},
		{"supervisor", false},
		{"manager", false},
		{"admin", true},
		{"superadmin", true},
	}
	for _, k := range kes {
		t.Run(k.roleKey, func(t *testing.T) {
			userID := seedMember(t, ctx, pool, k.roleKey, "approved")
			code, body := callDashboard(t, pool, userID)
			if code != http.StatusOK {
				t.Fatalf("kod = %d, mahu 200", code)
			}
			ada := body["admin"] != nil
			if ada != k.mahuBlok {
				t.Errorf("admin ada = %v, mahu %v untuk role %q", ada, k.mahuBlok, k.roleKey)
			}
		})
	}
}

func TestDashboardPendingApprovalsKiraAhliPending(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	adminID := seedMember(t, ctx, pool, "admin", "approved")

	_, sebelum := callDashboard(t, pool, adminID)
	asal := sebelum["admin"].(map[string]any)["pending_approvals"].(float64)

	seedMember(t, ctx, pool, "ahli", "pending")

	_, selepas := callDashboard(t, pool, adminID)
	kini := selepas["admin"].(map[string]any)["pending_approvals"].(float64)

	if kini != asal+1 {
		t.Errorf("pending_approvals = %v, mahu %v", kini, asal+1)
	}
}
```

- [ ] **Step 2: Jalankan ujian, sahkan ia GAGAL**

Run: `go test ./internal/http/handlers/ -run "TestDashboardBlokAdmin|TestDashboardPendingApprovals" -v`
Expected: GAGAL — `admin ada = false, mahu true untuk role "admin"`.

- [ ] **Step 3: Tambah query**

```sql
-- name: CountPendingMembers :one
select count(*) from profiles where status = 'pending';

-- name: CountNewMembersThisMonth :one
select count(*) from profiles
where approved_at >= date_trunc('month', now());

-- name: MemberStatsByDepartment :many
-- Tanpa had di sini - handler yang memotong kepada 6 teratas + baris
-- "Lain-lain", supaya jumlah keseluruhan kekal tepat.
select coalesce(d.code, '') as code,
       coalesce(d.name, 'Tiada bahagian') as name,
       count(*) as count
from profiles p
left join departments d on d.code = p.department_code
where p.status = 'approved' and p.active = true
group by d.code, d.name
order by count desc;

-- name: ActivityStatsThisMonth :one
-- attendance_rate: kehadiran direkod bagi sesi yang SUDAH TAMAT dalam
-- bulan semasa, dibahagi pendaftaran aktif pada aktiviti sesi-sesi itu.
-- Sesi belum tamat dikecualikan supaya kadar tidak nampak rendah palsu
-- sepanjang bulan berjalan. Pembahagi sifar -> null (bukan 0).
select
  (select count(*) from activities
    where deleted_at is null and status = 'published' and ends_at >= now())
    as upcoming,
  (select count(*) from activity_registrations
    where status <> 'cancelled' and registered_at >= date_trunc('month', now()))
    as registrations_this_month,
  (select case when denom.jumlah = 0 then null
               else numer.jumlah::float8 / denom.jumlah::float8 end
   from
     (select count(*) as jumlah from activity_attendances at
       join activity_sessions s on s.id = at.session_id
      where s.ends_at >= date_trunc('month', now()) and s.ends_at < now()) numer,
     (select count(*) as jumlah from activity_registrations r
       where r.status <> 'cancelled'
         and r.activity_id in (
           select s.activity_id from activity_sessions s
            where s.ends_at >= date_trunc('month', now()) and s.ends_at < now())) denom
  ) as attendance_rate;
```

Sahkan nama lajur sebenar sebelum jalankan: `\d departments` (`code`/`name`), `\d profiles` (`department_code`, `approved_at`, `active`), `\d activity_attendances` (`session_id`), `\d activity_sessions` (`ends_at`, `activity_id`), `\d activity_registrations` (`registered_at`). Betulkan query kepada nama sebenar — jangan biarkan tekaan masuk.

- [ ] **Step 4: Jana kod sqlc**

Run: `sqlc generate`
Expected: empat query baharu muncul; perhatikan jenis Go yang dijana untuk `attendance_rate` (kemungkinan besar `pgtype.Float8` kerana nullable).

- [ ] **Step 5: Isi blok admin**

Ganti `type adminBlock struct{}` dalam `dashboard.go`:

```go
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

type adminBlock struct {
	PendingApprovals int64         `json:"pending_approvals"`
	MemberStats      memberStats   `json:"member_stats"`
	ActivityStats    activityStats `json:"activity_stats"`
}

// maxDepartmentRows - kad memuatkan segelintir baris; selebihnya
// digabung sebagai "Lain-lain" supaya jumlah kekal tepat tanpa
// menghantar 40 baris untuk sebuah carta kecil.
const maxDepartmentRows = 6
```

Dalam `Get`, ganti `Admin: nil` dengan panggilan helper:

```go
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
```

dan tambah:

```go
// adminRoleKey - siling blok statistik dashboard. Padanan
// superAdminRoleKey dalam payments.go; rank sebenar ditentukan oleh
// jadual `roles`, bukan pemalar ini.
const adminRoleKey = "admin"

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

	var kadar *float64
	if act.AttendanceRate.Valid {
		v := act.AttendanceRate.Float64
		kadar = &v
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
	}, nil
}
```

Tambah `"context"`, `"github.com/google/uuid"` dan `"marc/internal/authz"` pada import.

- [ ] **Step 6: Jalankan ujian, sahkan ia LULUS**

Run: `go test ./internal/http/handlers/ -run TestDashboard -v`
Expected: PASS untuk semua ujian dashboard, termasuk kelima-lima sub-ujian role.

- [ ] **Step 7: Stage**

```bash
git add queries/dashboard.sql internal/db/sqlc/ \
        internal/http/handlers/dashboard.go \
        internal/http/handlers/dashboard_live_test.go
```

---

### Task 4: Kutipan bulan ini + peraturan derma superadmin

Deliverable: kad kutipan yang tidak pernah menunjukkan angka derma kepada admin bukan-superadmin.

**Files:**
- Modify: `marc_go/queries/dashboard.sql`
- Modify: `marc_go/internal/http/handlers/dashboard.go`
- Modify: `marc_go/internal/http/handlers/dashboard_live_test.go`

**Interfaces:**
- Consumes: `adminBlock` daripada Task 3; `superAdminRoleKey` (`payments.go:415`); helper ujian `seedDonation(t, pool, *uuid.UUID, status, amountCents)` (`my_payments_live_test.go:23`).
- Produces: medan `RevenueThisMonth revenueBlock` pada `adminBlock`.

- [ ] **Step 1: Tulis ujian yang gagal**

```go
func TestDashboardDermaSuperadminSahaja(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	adminID := seedMember(t, ctx, pool, "admin", "approved")
	superID := seedMember(t, ctx, pool, "superadmin", "approved")
	penderma := seedMember(t, ctx, pool, "ahli", "approved")
	seedDonation(t, pool, &penderma, "succeeded", 5000)

	_, badanAdmin := callDashboard(t, pool, adminID)
	revAdmin := badanAdmin["admin"].(map[string]any)["revenue_this_month"].(map[string]any)
	if revAdmin["donation_cents"] != nil {
		t.Errorf("donation_cents = %v untuk admin, mahu null", revAdmin["donation_cents"])
	}

	_, badanSuper := callDashboard(t, pool, superID)
	revSuper := badanSuper["admin"].(map[string]any)["revenue_this_month"].(map[string]any)
	if revSuper["donation_cents"] == nil {
		t.Fatalf("donation_cents null untuk superadmin, mahu angka")
	}
	if revSuper["donation_cents"].(float64) < 5000 {
		t.Errorf("donation_cents = %v, mahu >= 5000", revSuper["donation_cents"])
	}

	// total_cents admin MESTI mengecualikan derma yang superadmin nampak.
	if revAdmin["total_cents"].(float64) >= revSuper["total_cents"].(float64) {
		t.Errorf("total admin (%v) sepatutnya kurang drpd total superadmin (%v)",
			revAdmin["total_cents"], revSuper["total_cents"])
	}
}

func TestDashboardKutipanAbaikanBayaranGagal(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	superID := seedMember(t, ctx, pool, "superadmin", "approved")
	penderma := seedMember(t, ctx, pool, "ahli", "approved")

	_, sebelum := callDashboard(t, pool, superID)
	asal := sebelum["admin"].(map[string]any)["revenue_this_month"].(map[string]any)["donation_cents"].(float64)

	seedDonation(t, pool, &penderma, "failed", 9900)
	seedDonation(t, pool, &penderma, "pending", 8800)

	_, selepas := callDashboard(t, pool, superID)
	kini := selepas["admin"].(map[string]any)["revenue_this_month"].(map[string]any)["donation_cents"].(float64)

	if kini != asal {
		t.Errorf("donation_cents = %v, mahu kekal %v (failed/pending tidak dikira)", kini, asal)
	}
}
```

- [ ] **Step 2: Jalankan ujian, sahkan ia GAGAL**

Run: `go test ./internal/http/handlers/ -run "TestDashboardDerma|TestDashboardKutipan" -v`
Expected: GAGAL dengan panik penegasan jenis pada `revenue_this_month` (medan belum wujud) — itu kegagalan yang betul untuk peringkat ini.

- [ ] **Step 3: Tambah query**

```sql
-- name: SumRegistrationRevenueThisMonth :one
select coalesce(sum(amount_cents), 0)::bigint
from registration_payments
where status = 'succeeded' and created_at >= date_trunc('month', now());

-- name: SumActivityRevenueThisMonth :one
-- fee_cents_paid = snapshot amaun yang BENAR-BENAR dibayar; sengaja
-- BUKAN activities.fee_cents hidup (yuran boleh ditukar selepas bayar).
select coalesce(sum(fee_cents_paid), 0)::bigint
from activity_registrations
where payment_status = 'paid'
  and fee_cents_paid is not null
  and registered_at >= date_trunc('month', now());

-- name: SumDonationRevenueThisMonth :one
select coalesce(sum(amount_cents), 0)::bigint
from donations
where status = 'succeeded' and created_at >= date_trunc('month', now());
```

Sahkan nilai sebenar `activity_registrations.payment_status` untuk "sudah bayar" (`\d activity_registrations`, atau baca `ListMyActivityPayments`) — kalau ia `'succeeded'` dan bukan `'paid'`, betulkan.

- [ ] **Step 4: Jana kod sqlc**

Run: `sqlc generate`
Expected: tiga query jumlah muncul, kesemuanya memulangkan `int64`.

- [ ] **Step 5: Isi kutipan**

Tambah pada `dashboard.go`:

```go
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
```

Tambah `RevenueThisMonth revenueBlock \`json:"revenue_this_month"\`` pada `adminBlock`, dan dalam `buildAdminBlock`:

```go
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
```

dan hantar `RevenueThisMonth: revenue` dalam nilai pulangan.

- [ ] **Step 6: Jalankan ujian, sahkan ia LULUS**

Run: `go test ./internal/http/handlers/ -run TestDashboard -v`
Expected: PASS untuk semua ujian dashboard.

- [ ] **Step 7: Jalankan suite penuh**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: tiada regresi. Ujian yang memerlukan DB akan dilangkau melainkan DSN ditetapkan — jalankan sekurang-kurangnya `./internal/http/handlers/` dengan DSN ditetapkan.

- [ ] **Step 8: Kemas kini TODO.md dan stage**

Tambah nota ringkas dalam `TODO.md` bahawa `GET /dashboard` sudah wujud dan merujuk spec.

```bash
git add queries/dashboard.sql internal/db/sqlc/ \
        internal/http/handlers/dashboard.go \
        internal/http/handlers/dashboard_live_test.go TODO.md
```

---

### Task 5: `outstanding_registration_fee_cents`

**Prasyarat DIPENUHI (disahkan 2026-09-05).** Kerja Staff ID sudah
mendarat pada `staging` (HEAD `08f704a`), dan `/me/payments` sudah
memulangkan `outstanding_registration_fee` (`payments.go:365`, pembantu
di `profile.go:1537`). Task ini kini berjalan seperti task biasa
mengikut urutannya. Langkah 1 di bawah kekal sebagai semakan pengesahan.

**Files:**
- Modify: `marc_go/internal/http/handlers/dashboard.go`
- Modify: `marc_go/internal/http/handlers/dashboard_live_test.go`

**Interfaces:**
- Consumes: helper yuran tertunggak yang dihasilkan kerja Staff ID (cari dalam `payments.go` / `registration_payment.go` selepas ia mendarat — nama sebenar belum wujud pada tarikh pelan ini).
- Produces: medan `OutstandingRegistrationFeeCents *int64` pada `membershipBlock`.

- [ ] **Step 1: Cari helper sebenar**

Run: `cd /Users/hafiz/Developments/marc_go && grep -rn "outstanding_registration_fee\|OutstandingRegistrationFee" internal/ | head`
Expected: sekurang-kurangnya satu padanan dalam kod bukan-ujian. **Kalau tiada padanan, kerja Staff ID belum mendarat — berhenti dan laporkan.**

- [ ] **Step 2: Tulis ujian yang gagal**

```go
func TestDashboardYuranTertunggakPadanMePayments(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()
	userID := seedMember(t, ctx, pool, "ahli", "approved")

	_, dash := callDashboard(t, pool, userID)
	membership := dash["member"].(map[string]any)["membership"].(map[string]any)
	dariDashboard := membership["outstanding_registration_fee_cents"]

	me := minePayments(t, pool, userID)
	dariPayments := me["outstanding_registration_fee"]

	if dariDashboard != dariPayments {
		t.Errorf("dashboard = %v, /me/payments = %v - dua sumber kebenaran menyimpang",
			dariDashboard, dariPayments)
	}
}
```

Padankan nama kunci `dariPayments` dengan yang benar-benar dipulangkan `/me/payments` selepas kerja Staff ID mendarat.

- [ ] **Step 3: Jalankan ujian, sahkan ia GAGAL**

Run: `go test ./internal/http/handlers/ -run TestDashboardYuranTertunggak -v`
Expected: GAGAL — `dashboard = <nil>, /me/payments = <angka>`.

- [ ] **Step 4: Guna semula helper itu**

Tambah `OutstandingRegistrationFeeCents *int64 \`json:"outstanding_registration_fee_cents"\`` pada `membershipBlock`, dan isikannya dengan **memanggil helper yang sama** yang digunakan `/me/payments`. Jangan salin logiknya, jangan tulis query baharu — dua tempat yang mengira "berhutang atau tidak" secara berasingan akan menyimpang, dan itulah tepat-tepat yang ujian di atas menghalang.

- [ ] **Step 5: Jalankan ujian, sahkan ia LULUS**

Run: `go test ./internal/http/handlers/ -run TestDashboard -v`
Expected: PASS.

- [ ] **Step 6: Stage**

```bash
git add internal/http/handlers/dashboard.go internal/http/handlers/dashboard_live_test.go
```

---

# BAHAGIAN B — marc_flutter

### Task 6: Model dashboard + penghuraian defensif

Deliverable: lapisan model tulen yang tidak pernah crash pada payload yang tidak diketahui, dengan ujian unit sahaja (tiada widget, tiada rangkaian).

**Files:**
- Create: `marc_flutter/lib/features/dashboard/dashboard_models.dart`
- Create: `marc_flutter/test/features/dashboard/dashboard_models_test.dart`

**Interfaces:**
- Consumes: tiada (lapisan paling bawah).
- Produces: `DashboardData.fromJson(Map<String, dynamic>)` dengan medan `member` (`MemberBlock`) dan `admin` (`AdminBlock?`); `MemberBlock` dengan `upcomingRegistrations`, `openActivities`, `membership`, `unreadNotifications`, `certificatesTotal`, `totalMembers`; `AdminBlock` dengan `pendingApprovals`, `revenue`, `memberStats`, `activityStats`. Task 7-9 mengimport kesemuanya.

- [ ] **Step 1: Tulis ujian yang gagal**

Cipta `test/features/dashboard/dashboard_models_test.dart`:

```dart
import 'package:flutter_test/flutter_test.dart';
import 'package:marc/features/dashboard/dashboard_models.dart';

void main() {
  group('DashboardData.fromJson', () {
    test('payload minimum tanpa kunci admin → admin null, senarai kosong', () {
      final data = DashboardData.fromJson({
        'member': {
          'membership': {'status': 'approved'},
        },
      });

      expect(data.admin, isNull);
      expect(data.member.upcomingRegistrations, isEmpty);
      expect(data.member.openActivities, isEmpty);
      expect(data.member.unreadNotifications, 0);
      expect(data.member.certificatesTotal, 0);
      expect(data.member.totalMembers, 0);
      expect(data.member.membership.status, 'approved');
      expect(data.member.membership.memberId, isNull);
      expect(data.member.membership.outstandingFeeCents, isNull);
    });

    test('medan tidak dikenali diabaikan, tidak melontar', () {
      expect(
        () => DashboardData.fromJson({
          'member': {
            'membership': {'status': 'approved', 'medan_masa_depan': 42},
            'medan_baharu': {'apa': 'ini'},
          },
          'admin': null,
          'aras_atas_baharu': [1, 2, 3],
        }),
        returnsNormally,
      );
    });

    test('blok admin dihurai penuh', () {
      final data = DashboardData.fromJson({
        'member': {
          'membership': {'status': 'approved'},
        },
        'admin': {
          'pending_approvals': 5,
          'revenue_this_month': {
            'currency': 'MYR',
            'registration_cents': 30000,
            'activity_cents': 12500,
            'donation_cents': null,
            'total_cents': 42500,
          },
          'member_stats': {
            'active': 312,
            'pending': 5,
            'new_this_month': 18,
            'by_department': [
              {'code': 'KL', 'name': 'Kuala Lumpur', 'count': 90},
            ],
          },
          'activity_stats': {
            'upcoming': 4,
            'registrations_this_month': 87,
            'attendance_rate': 0.72,
          },
        },
      });

      final admin = data.admin!;
      expect(admin.pendingApprovals, 5);
      expect(admin.revenue.donationCents, isNull);
      expect(admin.revenue.totalCents, 42500);
      expect(admin.memberStats.byDepartment.single.name, 'Kuala Lumpur');
      expect(admin.activityStats.attendanceRate, 0.72);
    });

    test('attendance_rate null dikekalkan sebagai null (bukan 0)', () {
      final data = DashboardData.fromJson({
        'member': {
          'membership': {'status': 'approved'},
        },
        'admin': {
          'activity_stats': {'attendance_rate': null},
        },
      });

      expect(data.admin!.activityStats.attendanceRate, isNull);
    });

    test('aktiviti dihurai dengan tarikh', () {
      final data = DashboardData.fromJson({
        'member': {
          'membership': {'status': 'approved'},
          'upcoming_registrations': [
            {
              'id': 'r1',
              'activity_id': 'a1',
              'title': 'Bengkel',
              'starts_at': '2026-09-10T09:00:00Z',
              'ends_at': '2026-09-10T17:00:00Z',
              'category_name': 'Latihan',
              'payment_status': 'paid',
            },
          ],
        },
      });

      final reg = data.member.upcomingRegistrations.single;
      expect(reg.activityId, 'a1');
      expect(reg.startsAt.toUtc().hour, 9);
    });
  });
}
```

- [ ] **Step 2: Jalankan ujian, sahkan ia GAGAL**

Run: `cd /Users/hafiz/Developments/marc_flutter && flutter test test/features/dashboard/dashboard_models_test.dart`
Expected: GAGAL — `Target of URI doesn't exist: 'package:marc/features/dashboard/dashboard_models.dart'`.

- [ ] **Step 3: Tulis model**

Cipta `lib/features/dashboard/dashboard_models.dart`:

```dart
/// Model skrin Utama (`GET /dashboard`). Penghuraian di sini SENGAJA
/// defensif pada setiap medan: payload dashboard ialah yang paling kerap
/// berubah bentuk antara keluaran, dan app lama di telefon ahli mesti
/// terus berfungsi bila backend menambah atau menyusun semula medan.
/// Kontrak penuh: marc_go docs/superpowers/specs/2026-09-05-dashboard-home-design.md
library;

int _int(Object? v) => switch (v) {
  final int i => i,
  final num n => n.toInt(),
  _ => 0,
};

int? _intOrNull(Object? v) => switch (v) {
  final int i => i,
  final num n => n.toInt(),
  _ => null,
};

double? _doubleOrNull(Object? v) => switch (v) {
  final num n => n.toDouble(),
  _ => null,
};

String _str(Object? v) => v is String ? v : '';

String? _strOrNull(Object? v) => switch (v) {
  final String s when s.trim().isEmpty => null,
  final String s => s,
  _ => null,
};

/// Tarikh tak sah/hilang → epoch, BUKAN lontaran: satu tarikh rosak pada
/// satu kad tidak sepatutnya mengosongkan seluruh skrin Utama.
DateTime _date(Object? v) =>
    DateTime.tryParse(v is String ? v : '') ?? DateTime.fromMillisecondsSinceEpoch(0);

Map<String, dynamic> _map(Object? v) =>
    v is Map<String, dynamic> ? v : const <String, dynamic>{};

List<Map<String, dynamic>> _list(Object? v) => v is List
    ? v.whereType<Map<String, dynamic>>().toList(growable: false)
    : const [];

class Membership {
  const Membership({
    required this.status,
    this.memberId,
    this.staffIdVerified = false,
    this.outstandingFeeCents,
  });

  final String status;
  final String? memberId;
  final bool staffIdVerified;

  /// Null = tiada yuran tertunggak. Backend ialah satu-satunya hakim di
  /// sini - client TIDAK mengira semula kelayakan yuran.
  final int? outstandingFeeCents;

  bool get isApproved => status == 'approved';

  factory Membership.fromJson(Map<String, dynamic> json) => Membership(
    status: _str(json['status']),
    memberId: _strOrNull(json['member_id']),
    staffIdVerified: json['staff_id_verified'] == true,
    outstandingFeeCents: _intOrNull(json['outstanding_registration_fee_cents']),
  );
}

class UpcomingRegistration {
  const UpcomingRegistration({
    required this.id,
    required this.activityId,
    required this.title,
    required this.startsAt,
    required this.endsAt,
    required this.categoryName,
    required this.paymentStatus,
  });

  final String id;
  final String activityId;
  final String title;
  final DateTime startsAt;
  final DateTime endsAt;
  final String categoryName;
  final String paymentStatus;

  factory UpcomingRegistration.fromJson(Map<String, dynamic> json) =>
      UpcomingRegistration(
        id: _str(json['id']),
        activityId: _str(json['activity_id']),
        title: _str(json['title']),
        startsAt: _date(json['starts_at']),
        endsAt: _date(json['ends_at']),
        categoryName: _str(json['category_name']),
        paymentStatus: _str(json['payment_status']),
      );
}

class OpenActivity {
  const OpenActivity({
    required this.id,
    required this.title,
    required this.startsAt,
    required this.categoryName,
    required this.feeCents,
    required this.currency,
    required this.registrationCount,
  });

  final String id;
  final String title;
  final DateTime startsAt;
  final String categoryName;
  final int feeCents;
  final String currency;
  final int registrationCount;

  factory OpenActivity.fromJson(Map<String, dynamic> json) => OpenActivity(
    id: _str(json['id']),
    title: _str(json['title']),
    startsAt: _date(json['starts_at']),
    categoryName: _str(json['category_name']),
    feeCents: _int(json['fee_cents']),
    currency: _str(json['currency']),
    registrationCount: _int(json['registration_count']),
  );
}

class MemberBlock {
  const MemberBlock({
    required this.membership,
    required this.upcomingRegistrations,
    required this.openActivities,
    required this.unreadNotifications,
    required this.certificatesTotal,
    required this.totalMembers,
  });

  final Membership membership;
  final List<UpcomingRegistration> upcomingRegistrations;
  final List<OpenActivity> openActivities;
  final int unreadNotifications;
  final int certificatesTotal;
  final int totalMembers;

  factory MemberBlock.fromJson(Map<String, dynamic> json) => MemberBlock(
    membership: Membership.fromJson(_map(json['membership'])),
    upcomingRegistrations: _list(json['upcoming_registrations'])
        .map(UpcomingRegistration.fromJson)
        .toList(growable: false),
    openActivities: _list(json['open_activities'])
        .map(OpenActivity.fromJson)
        .toList(growable: false),
    unreadNotifications: _int(json['unread_notifications']),
    certificatesTotal: _int(json['certificates_total']),
    totalMembers: _int(json['total_members']),
  );
}

class Revenue {
  const Revenue({
    required this.currency,
    required this.registrationCents,
    required this.activityCents,
    required this.totalCents,
    this.donationCents,
  });

  final String currency;
  final int registrationCents;
  final int activityCents;
  final int totalCents;

  /// Null = pemanggil bukan superadmin, jadi derma DIKECUALIKAN daripada
  /// [totalCents] juga. Kad mesti menyatakan itu, bukan memapar jumlah
  /// yang kelihatan lengkap.
  final int? donationCents;

  bool get includesDonations => donationCents != null;

  factory Revenue.fromJson(Map<String, dynamic> json) => Revenue(
    currency: _str(json['currency']),
    registrationCents: _int(json['registration_cents']),
    activityCents: _int(json['activity_cents']),
    totalCents: _int(json['total_cents']),
    donationCents: _intOrNull(json['donation_cents']),
  );
}

class DepartmentStat {
  const DepartmentStat({required this.code, required this.name, required this.count});

  final String code;
  final String name;
  final int count;

  factory DepartmentStat.fromJson(Map<String, dynamic> json) => DepartmentStat(
    code: _str(json['code']),
    name: _str(json['name']),
    count: _int(json['count']),
  );
}

class MemberStats {
  const MemberStats({
    required this.active,
    required this.pending,
    required this.newThisMonth,
    required this.byDepartment,
  });

  final int active;
  final int pending;
  final int newThisMonth;
  final List<DepartmentStat> byDepartment;

  factory MemberStats.fromJson(Map<String, dynamic> json) => MemberStats(
    active: _int(json['active']),
    pending: _int(json['pending']),
    newThisMonth: _int(json['new_this_month']),
    byDepartment: _list(json['by_department'])
        .map(DepartmentStat.fromJson)
        .toList(growable: false),
  );
}

class ActivityStats {
  const ActivityStats({
    required this.upcoming,
    required this.registrationsThisMonth,
    this.attendanceRate,
  });

  final int upcoming;
  final int registrationsThisMonth;

  /// 0..1, atau null bila pembahagi sifar (tiada sesi tamat bulan ini).
  /// Null BUKAN sama dengan 0 - kad papar "—", bukan "0%".
  final double? attendanceRate;

  factory ActivityStats.fromJson(Map<String, dynamic> json) => ActivityStats(
    upcoming: _int(json['upcoming']),
    registrationsThisMonth: _int(json['registrations_this_month']),
    attendanceRate: _doubleOrNull(json['attendance_rate']),
  );
}

class AdminBlock {
  const AdminBlock({
    required this.pendingApprovals,
    required this.revenue,
    required this.memberStats,
    required this.activityStats,
  });

  final int pendingApprovals;
  final Revenue revenue;
  final MemberStats memberStats;
  final ActivityStats activityStats;

  factory AdminBlock.fromJson(Map<String, dynamic> json) => AdminBlock(
    pendingApprovals: _int(json['pending_approvals']),
    revenue: Revenue.fromJson(_map(json['revenue_this_month'])),
    memberStats: MemberStats.fromJson(_map(json['member_stats'])),
    activityStats: ActivityStats.fromJson(_map(json['activity_stats'])),
  );
}

class DashboardData {
  const DashboardData({required this.member, this.admin});

  final MemberBlock member;

  /// Null = pemanggil bukan admin. Client TIDAK menentukan kelayakan
  /// sendiri - kehadiran blok ini ialah keputusan backend.
  final AdminBlock? admin;

  factory DashboardData.fromJson(Map<String, dynamic> json) => DashboardData(
    member: MemberBlock.fromJson(_map(json['member'])),
    admin: json['admin'] == null ? null : AdminBlock.fromJson(_map(json['admin'])),
  );
}
```

- [ ] **Step 4: Jalankan ujian, sahkan ia LULUS**

Run: `flutter test test/features/dashboard/dashboard_models_test.dart`
Expected: PASS, kelima-lima ujian.

- [ ] **Step 5: Analisis**

Run: `flutter analyze lib/features/dashboard test/features/dashboard`
Expected: `No issues found!`

- [ ] **Step 6: Stage**

```bash
cd /Users/hafiz/Developments/marc_flutter
git add lib/features/dashboard/dashboard_models.dart \
        test/features/dashboard/dashboard_models_test.dart
```

---

### Task 7: Provider + ekstrak `EmailNotVerifiedView`

Deliverable: `dashboardProvider` yang tidak pernah memanggil `/dashboard` untuk ahli belum diluluskan, dan satu widget "sahkan emel" yang dikongsi Feed dan Dashboard.

**Files:**
- Create: `marc_flutter/lib/features/dashboard/dashboard_providers.dart`
- Create: `marc_flutter/lib/shared/ui/widgets/email_not_verified_view.dart`
- Modify: `marc_flutter/lib/features/posts/feed_page.dart` (buang `_EmailNotVerifiedView`, guna yang kongsi)

**Interfaces:**
- Consumes: `DashboardData` (Task 6); `dioProvider` (`lib/core/api_client.dart`); `myProfileProvider` (`lib/features/profile/profile_providers.dart`).
- Produces: `dashboardProvider` (`FutureProvider<DashboardData?>` — `null` bermakna "belum layak memanggil", bukan ralat); `EmailNotVerifiedView({required Future<void> Function() onRefresh})`.

- [ ] **Step 1: Ekstrak widget emel dahulu (refactor tulen)**

Pindahkan `_EmailNotVerifiedView` (dan `_EmailNotVerifiedViewState`) daripada `lib/features/posts/feed_page.dart:295-382` ke `lib/shared/ui/widgets/email_not_verified_view.dart`, dinamakan semula `EmailNotVerifiedView` (buang garis bawah), kekalkan tingkah lakunya **sama persis**. Tambah komen kepala:

```dart
/// Paparan "sahkan emel dahulu" - dikongsi Feed dan Dashboard. Diekstrak
/// daripada feed_page.dart bila Dashboard jadi tab Utama: dua salinan
/// borang hantar-semula-emel akan menyimpang, dan borang itu ialah
/// satu-satunya jalan keluar untuk ahli approved yang belum sahkan emel.
```

Kemas kini `feed_page.dart` untuk mengimport dan menggunakannya.

- [ ] **Step 2: Sahkan refactor tidak mengubah apa-apa**

Run: `flutter analyze lib && flutter test`
Expected: analisis bersih; semua ujian sedia ada masih lulus. Refactor tulen — kalau ada ujian gagal, tingkah laku berubah; betulkan sebelum meneruskan.

- [ ] **Step 3: Tulis ujian provider yang gagal**

Cipta `test/features/dashboard/dashboard_provider_test.dart`:

```dart
import 'dart:convert';
import 'dart:typed_data';

import 'package:dio/dio.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:marc/core/api_client.dart';
import 'package:marc/features/dashboard/dashboard_providers.dart';
import 'package:marc/features/profile/profile_providers.dart';

import '../../support/profile_fixtures.dart';

class _RecordingAdapter implements HttpClientAdapter {
  final List<String> paths = [];

  @override
  void close({bool force = false}) {}

  @override
  Future<ResponseBody> fetch(
    RequestOptions options,
    Stream<Uint8List>? requestStream,
    Future<void>? cancelFuture,
  ) async {
    paths.add(options.path);
    return ResponseBody.fromString(
      jsonEncode({
        'member': {
          'membership': {'status': 'approved'},
          'total_members': 7,
        },
        'admin': null,
      }),
      200,
      headers: {
        Headers.contentTypeHeader: [Headers.jsonContentType],
      },
    );
  }
}

void main() {
  test('ahli approved → /dashboard dipanggil', () async {
    final adapter = _RecordingAdapter();
    final dio = Dio()..httpClientAdapter = adapter;
    final container = ProviderContainer(
      overrides: [
        dioProvider.overrideWithValue(dio),
        myProfileProvider.overrideWith((ref) async => approvedProfile),
      ],
    );
    addTearDown(container.dispose);

    final data = await container.read(dashboardProvider.future);

    expect(adapter.paths, ['/dashboard']);
    expect(data!.member.totalMembers, 7);
  });

  test('ahli pending → TIADA panggilan rangkaian, data null', () async {
    final adapter = _RecordingAdapter();
    final dio = Dio()..httpClientAdapter = adapter;
    final container = ProviderContainer(
      overrides: [
        dioProvider.overrideWithValue(dio),
        myProfileProvider.overrideWith((ref) async => pendingProfile),
      ],
    );
    addTearDown(container.dispose);

    final data = await container.read(dashboardProvider.future);

    expect(adapter.paths, isEmpty, reason: '403 yang boleh diramal tidak patut dipanggil');
    expect(data, isNull);
  });
}
```

Cipta juga `test/support/profile_fixtures.dart` dengan dua pemalar `Profile` (`approvedProfile`, `pendingProfile`) — salin bentuk pemalar `_member` dalam `test/features/notifications/notifications_page_test.dart:48-64` dan padankan dengan medan sebenar kelas `Profile`. Fixture diletak dalam `test/support/` supaya Task 8-9 boleh guna semula.

- [ ] **Step 4: Jalankan ujian, sahkan ia GAGAL**

Run: `flutter test test/features/dashboard/dashboard_provider_test.dart`
Expected: GAGAL — URI `dashboard_providers.dart` tidak wujud.

- [ ] **Step 5: Tulis provider**

Cipta `lib/features/dashboard/dashboard_providers.dart`:

```dart
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:marc/core/api_client.dart';
import 'package:marc/features/dashboard/dashboard_models.dart';
import 'package:marc/features/profile/profile_providers.dart';

/// Data skrin Utama. SATU panggilan untuk seluruh skrin - lihat spec
/// untuk sebab satu endpoint dipilih berbanding komposisi client-side.
///
/// Memulangkan `null` (bukan melontar) bila ahli belum diluluskan:
/// backend meletakkan `/dashboard` dalam kumpulan `approved`, jadi
/// memanggilnya untuk ahli pending ialah 403 yang boleh diramal 100%.
/// Skrin memapar `PendingStatusView` dalam keadaan itu dan tidak pernah
/// membaca data ini.
final dashboardProvider = FutureProvider<DashboardData?>((ref) async {
  final profile = await ref.watch(myProfileProvider.future);
  if (profile?.status != 'approved') return null;

  final dio = ref.watch(dioProvider);
  final res = await dio.get<Map<String, dynamic>>('/dashboard');
  return DashboardData.fromJson(res.data ?? const {});
});
```

Kalau `myProfileProvider` bukan `FutureProvider<Profile?>`, padankan bentuk sebenarnya (baca `profile_providers.dart`) dan bukan tandatangan yang diandaikan di sini.

- [ ] **Step 6: Jalankan ujian, sahkan ia LULUS**

Run: `flutter test test/features/dashboard/`
Expected: PASS untuk ujian model dan provider.

- [ ] **Step 7: Stage**

```bash
git add lib/features/dashboard/dashboard_providers.dart \
        lib/shared/ui/widgets/email_not_verified_view.dart \
        lib/features/posts/feed_page.dart \
        test/features/dashboard/dashboard_provider_test.dart \
        test/support/profile_fixtures.dart
```

---

### Task 8: `DashboardPage` — gate, keadaan loading/ralat, kad ahli

Deliverable: skrin dashboard lengkap untuk ahli biasa, termasuk gate pending/emel yang dahulunya hanya ada pada Feed.

**Files:**
- Create: `marc_flutter/lib/features/dashboard/dashboard_page.dart`
- Create: `marc_flutter/lib/features/dashboard/widgets/member_cards.dart`
- Create: `marc_flutter/test/features/dashboard/dashboard_page_test.dart`

**Interfaces:**
- Consumes: `dashboardProvider`, `DashboardData` (Task 6-7); `PendingStatusView` (widget kongsi sedia ada, sama seperti digunakan `feed_page.dart:107`); `EmailNotVerifiedView` (Task 7); `myProfileProvider`.
- Produces: `DashboardPage` (widget tanpa argumen, dipasang pada route `/dashboard` dalam Task 10).

- [ ] **Step 1: Tulis ujian widget yang gagal**

Cipta `test/features/dashboard/dashboard_page_test.dart` dengan corak harness `notifications_page_test.dart:99-137` (`_FakeAdapter`, `_LoggedInAuthNotifier`, `ProviderScope` dengan override `dioProvider`/`myProfileProvider`/`routerProvider`):

```dart
void main() {
  testWidgets('ahli pending → PendingStatusView, /dashboard tidak dipanggil', (tester) async {
    final dipanggil = <String>[];
    final dio = _dioYangMerekod(dipanggil);

    await tester.pumpWidget(_app(dio, _testRouter(), profile: pendingProfile));
    await tester.pumpAndSettle();

    expect(find.byType(PendingStatusView), findsOneWidget);
    expect(dipanggil, isEmpty);
  });

  testWidgets('ahli approved tanpa blok admin → tiada kad admin', (tester) async {
    final dio = _dioYangMemulangkan(_payloadAhli());

    await tester.pumpWidget(_app(dio, _testRouter(), profile: approvedProfile));
    await tester.pumpAndSettle();

    expect(find.text('Aktiviti Saya'), findsOneWidget);
    expect(find.text('Menunggu Kelulusan'), findsNothing);
    expect(find.text('Kutipan Bulan Ini'), findsNothing);
  });

  testWidgets('kiraan ringkas terpapar', (tester) async {
    final dio = _dioYangMemulangkan(_payloadAhli(unread: 4, certs: 7, members: 312));

    await tester.pumpWidget(_app(dio, _testRouter(), profile: approvedProfile));
    await tester.pumpAndSettle();

    expect(find.text('4'), findsOneWidget);
    expect(find.text('7'), findsOneWidget);
    expect(find.text('312'), findsOneWidget);
  });

  testWidgets('ralat rangkaian → kad "Cuba lagi", skrin tidak kosong', (tester) async {
    final dio = _dioYangGagal();

    await tester.pumpWidget(_app(dio, _testRouter(), profile: approvedProfile));
    await tester.pumpAndSettle();

    expect(find.text('Cuba lagi'), findsOneWidget);
    // Pintasan navigasi kekal berguna walaupun tanpa data.
    expect(find.text('Notifikasi'), findsOneWidget);
  });
}
```

Pembantu yang mesti ada dalam fail yang sama (tulis semua ini, jangan
rujuk silang fail ujian lain — ujian mesti berdiri sendiri):

```dart
class _FakeAdapter implements HttpClientAdapter {
  _FakeAdapter(this._onFetch);
  final Future<ResponseBody> Function(RequestOptions options) _onFetch;
  @override
  void close({bool force = false}) {}
  @override
  Future<ResponseBody> fetch(
    RequestOptions options,
    Stream<Uint8List>? requestStream,
    Future<void>? cancelFuture,
  ) => _onFetch(options);
}

Dio _dioYangMemulangkan(Map<String, dynamic> body) {
  return Dio()
    ..httpClientAdapter = _FakeAdapter(
      (_) async => ResponseBody.fromString(
        jsonEncode(body),
        200,
        headers: {
          Headers.contentTypeHeader: [Headers.jsonContentType],
        },
      ),
    );
}

Dio _dioYangMerekod(List<String> dipanggil) {
  return Dio()
    ..httpClientAdapter = _FakeAdapter((options) async {
      dipanggil.add(options.path);
      return ResponseBody.fromString('{}', 200, headers: {
        Headers.contentTypeHeader: [Headers.jsonContentType],
      });
    });
}

Dio _dioYangGagal() {
  return Dio()
    ..httpClientAdapter = _FakeAdapter(
      (options) async => throw DioException.connectionError(
        requestOptions: options,
        reason: 'ujian: rangkaian mati',
      ),
    );
}

Map<String, dynamic> _payloadAhli({
  int unread = 0,
  int certs = 0,
  int members = 0,
}) => {
  'member': {
    'membership': {'status': 'approved', 'member_id': 'MARC-001'},
    'upcoming_registrations': const [],
    'open_activities': const [],
    'unread_notifications': unread,
    'certificates_total': certs,
    'total_members': members,
  },
  'admin': null,
};

GoRouter _testRouter() {
  Widget kosong(String label) => Scaffold(body: Text(label));
  return GoRouter(
    initialLocation: '/dashboard',
    routes: [
      GoRoute(path: '/dashboard', builder: (_, _) => const DashboardPage()),
      GoRoute(path: '/notifications', builder: (_, _) => kosong('notifikasi')),
      GoRoute(path: '/my-certificates', builder: (_, _) => kosong('sijil')),
      GoRoute(path: '/members', builder: (_, _) => kosong('ahli')),
      GoRoute(path: '/members/pending', builder: (_, _) => kosong('pending')),
      GoRoute(path: '/my-activities', builder: (_, _) => kosong('aktiviti-saya')),
      GoRoute(path: '/activities', builder: (_, _) => kosong('aktiviti')),
      GoRoute(path: '/admin/payments', builder: (_, _) => kosong('bayaran')),
      GoRoute(path: '/feed', builder: (_, _) => kosong('feed')),
    ],
  );
}

Widget _app(Dio dio, GoRouter router, {required Profile profile}) {
  return ProviderScope(
    overrides: [
      dioProvider.overrideWithValue(dio),
      authNotifierProvider.overrideWith((ref) => _LoggedInAuthNotifier()),
      myProfileProvider.overrideWith((ref) async => profile),
      routerProvider.overrideWithValue(router),
    ],
    child: MaterialApp.router(theme: AppTheme.light, routerConfig: router),
  );
}
```

`_LoggedInAuthNotifier` disalin daripada `notifications_page_test.dart:17-21`.
Import yang diperlukan: `dart:convert`, `dart:typed_data`, `package:dio/dio.dart`,
`package:flutter/material.dart`, `package:flutter_riverpod/flutter_riverpod.dart`,
`package:flutter_test/flutter_test.dart`, `package:go_router/go_router.dart`,
`package:marc/app/router.dart`, `package:marc/app/theme.dart`,
`package:marc/core/api_client.dart`, `package:marc/core/auth_state.dart`,
`package:marc/core/token_storage.dart`,
`package:marc/features/dashboard/dashboard_page.dart`,
`package:marc/features/profile/profile_providers.dart`, dan fixture
`../../support/profile_fixtures.dart` daripada Task 7.

- [ ] **Step 2: Jalankan ujian, sahkan ia GAGAL**

Run: `flutter test test/features/dashboard/dashboard_page_test.dart`
Expected: GAGAL — `DashboardPage` tidak wujud.

- [ ] **Step 3: Tulis kad ahli**

Cipta `lib/features/dashboard/widgets/member_cards.dart` dengan widget awam: `MembershipCard`, `UpcomingActivitiesCard` (tajuk `'Aktiviti Saya'`), `OpenActivitiesCard`, `QuickStatsRow` (tiga petak: `'Notifikasi'`/`'Sijil'`/`'Ahli'` dengan kiraan sebagai teks), setiap satu menerima potongan model yang diperlukan sahaja (`Membership`, `List<UpcomingRegistration>`, dsb.) — **bukan** seluruh `DashboardData`, supaya setiap kad boleh diuji secara berasingan.

`MembershipCard` memapar butang bayar bila `membership.outstandingFeeCents != null`; sehingga Task 5 mendarat medan itu sentiasa null dan butang itu tidak pernah muncul. Itu betul, bukan kod mati.

- [ ] **Step 4: Tulis DashboardPage**

Cipta `lib/features/dashboard/dashboard_page.dart`:

```dart
/// Skrin Utama app. Gate pending/emel di sini MENYALIN corak
/// feed_page.dart:58-140 dengan sengaja: Dashboard kini destinasi lalai
/// selepas log masuk, jadi ia mewarisi tanggungjawab Feed sebagai skrin
/// pertama yang dilihat ahli BELUM diluluskan.
```

Struktur `build`:

1. `ref.watch(myProfileProvider.select(...))` mengambil rekod `(status, emailVerified, isInitialLoading)` — sama persis seperti `feed_page.dart:74-98`.
2. `isInitialLoading` → `Scaffold(body: Center(child: CircularProgressIndicator.adaptive()))`.
   **Semakan ini WAJIB.** Tanpanya, `status` null semasa muat pertama tidak dapat dibezakan daripada null semasa ralat, dan dashboard penuh terpapar sekejap kepada ahli pending (bug sebenar 2026-08-24 pada Feed).
3. `status != null && status != 'approved'` → `PendingStatusView` (tandatangan sebenar: `pending_status_view.dart:17-24`):

```dart
      return PendingStatusView(
        status: profileStatus,
        registrationPaymentStatus: profileRegistrationPaymentStatus,
        registrationFeeCents: profileRegistrationFeeCents,
        onRefresh: () async {
          try {
            final refreshed = await ref.refresh(myProfileProvider.future);
            if (refreshed?.status == 'approved') {
              ref.invalidate(dashboardProvider);
            }
          } catch (_) {
            if (context.mounted) {
              MySnackBar.error(context, 'Gagal semak status. Cuba lagi.');
            }
          }
        },
      );
```

Ini bermakna `.select` pada langkah 1 mesti turut mengambil `registrationPaymentStatus` dan `registrationFeeCents` — rekod lima medan, sama persis seperti `feed_page.dart:74-98`.

4. `status == 'approved' && emailVerified == false` → `EmailNotVerifiedView(onRefresh: …)` dengan badan `onRefresh` yang sama seperti dalam `feed_page.dart:127-138`.
5. Selain itu: `ref.watch(dashboardProvider)` dan `switch` pada `AsyncValue` —
   - `loading` → skeleton berbentuk kad yang sama (`Shimmer`/placeholder kelabu), bukan spinner tengah skrin;
   - `error` → `_ErrorCard` dengan butang `'Cuba lagi'` → `ref.invalidate(dashboardProvider)`, diikuti `QuickStatsRow` dengan kiraan sifar supaya pintasan navigasi kekal boleh diketuk;
   - `data` → `RefreshIndicator` + `ListView` kad mengikut susunan spec (blok admin dahulu bila ada — dipasang dalam Task 9 — kemudian status keahlian, aktiviti saya, aktiviti terbuka, kiraan ringkas).

`dashboardProvider` menghasilkan `DashboardData?`, jadi cabang `data`
menerima nilai yang boleh jadi null. Null di sana bermakna "ahli belum
diluluskan" — keadaan yang gate pada langkah 3 sudah tangkap, jadi ia
tidak boleh dicapai. Tulisnya sebagai `data == null ? const SizedBox.shrink() : …`
dan **jangan** guna `!`: gate dan provider ialah dua semakan berasingan,
dan `!` di sini akan jadi crash kalau salah satu daripadanya berubah
kemudian.

Fail ini mengandungi **susun atur sahaja**; setiap kad hidup dalam `widgets/`.

- [ ] **Step 5: Jalankan ujian, sahkan ia LULUS**

Run: `flutter test test/features/dashboard/`
Expected: PASS untuk semua ujian dashboard.

- [ ] **Step 6: Analisis dan stage**

Run: `flutter analyze lib test`
Expected: `No issues found!`

```bash
git add lib/features/dashboard/ test/features/dashboard/
```

---

### Task 9: Blok admin pada skrin

Deliverable: pemanggil admin melihat empat kad statistik di atas kad ahli, dengan nota derma yang jujur.

**Files:**
- Create: `marc_flutter/lib/features/dashboard/widgets/admin_section.dart`
- Modify: `marc_flutter/lib/features/dashboard/dashboard_page.dart`
- Modify: `marc_flutter/test/features/dashboard/dashboard_page_test.dart`

**Interfaces:**
- Consumes: `AdminBlock`, `Revenue`, `MemberStats`, `ActivityStats` (Task 6).
- Produces: `AdminSection({required AdminBlock admin})`.

- [ ] **Step 1: Tulis ujian yang gagal**

```dart
testWidgets('blok admin terpapar bila admin != null', (tester) async {
  final dio = _dioYangMemulangkan(_payloadAdmin(pendingApprovals: 5));

  await tester.pumpWidget(_app(dio, _testRouter(), profile: approvedProfile));
  await tester.pumpAndSettle();

  expect(find.text('Menunggu Kelulusan'), findsOneWidget);
  expect(find.text('5'), findsOneWidget);
  expect(find.text('Kutipan Bulan Ini'), findsOneWidget);
  expect(find.text('Kuala Lumpur'), findsOneWidget);
});

testWidgets('donation_cents null → nota "tidak termasuk derma"', (tester) async {
  final dio = _dioYangMemulangkan(_payloadAdmin(donationCents: null));

  await tester.pumpWidget(_app(dio, _testRouter(), profile: approvedProfile));
  await tester.pumpAndSettle();

  expect(find.textContaining('tidak termasuk derma'), findsOneWidget);
});

testWidgets('donation_cents berisi → tiada nota pengecualian', (tester) async {
  final dio = _dioYangMemulangkan(_payloadAdmin(donationCents: 5000));

  await tester.pumpWidget(_app(dio, _testRouter(), profile: approvedProfile));
  await tester.pumpAndSettle();

  expect(find.textContaining('tidak termasuk derma'), findsNothing);
});

testWidgets('attendance_rate null → papar "—", bukan "0%"', (tester) async {
  final dio = _dioYangMemulangkan(_payloadAdmin(attendanceRate: null));

  await tester.pumpWidget(_app(dio, _testRouter(), profile: approvedProfile));
  await tester.pumpAndSettle();

  expect(find.text('0%'), findsNothing);
  expect(find.text('—'), findsWidgets);
});
```

Tambah pembantu `_payloadAdmin({int pendingApprovals = 0, int? donationCents, double? attendanceRate = 0.72})` yang membina payload penuh dengan satu bahagian bernama `'Kuala Lumpur'`.

- [ ] **Step 2: Jalankan ujian, sahkan ia GAGAL**

Run: `flutter test test/features/dashboard/dashboard_page_test.dart`
Expected: GAGAL — `Menunggu Kelulusan` tidak dijumpai.

- [ ] **Step 3: Tulis AdminSection**

Cipta `lib/features/dashboard/widgets/admin_section.dart` dengan empat kad:

- **Menunggu Kelulusan** — kiraan; guna warna amaran (`theme.extension<AppSemanticColors>()!.warning`, corak `approval_gate.dart:59`) bila > 0; ketukan → `/members/pending`.
- **Kutipan Bulan Ini** — `totalCents` sebagai jumlah utama, pecahan yuran pendaftaran/aktiviti di bawahnya, dan derma **hanya** bila `revenue.includesDonations`. Bila tidak, papar nota kecil `'Jumlah ini tidak termasuk derma'`. Ketukan → `/admin/payments`.
- **Statistik Ahli** — aktif / pending / baharu bulan ini + senarai `byDepartment`. Ketukan → `/members`.
- **Statistik Aktiviti** — akan datang, pendaftaran bulan ini, kadar kehadiran sebagai peratus; `attendanceRate == null` → `'—'`, **bukan** `'0%'`.

Formatkan sen kepada ringgit dengan pembantu format sedia ada kalau ada (cari `lib/shared` untuk pemformat mata wang sebelum menulis satu lagi).

- [ ] **Step 4: Pasang pada halaman**

Dalam `dashboard_page.dart`, dalam cabang `data`, sisip sebelum kad ahli:

```dart
    if (data.admin != null) AdminSection(admin: data.admin!),
```

- [ ] **Step 5: Jalankan ujian, sahkan ia LULUS**

Run: `flutter test test/features/dashboard/`
Expected: PASS untuk semua ujian, termasuk yang dari Task 8.

- [ ] **Step 6: Stage**

```bash
git add lib/features/dashboard/ test/features/dashboard/
```

---

### Task 10: Navigasi — 5 tab, dashboard sebagai Utama

Deliverable: app membuka dashboard selepas log masuk, dengan feed sebagai tab kedua; tiada laluan lama yang putus.

**Files:**
- Modify: `marc_flutter/lib/app/nav_shell.dart`
- Modify: `marc_flutter/lib/app/router.dart` (baris 78, 142, dan blok `StatefulShellRoute` ~266-292)
- Modify: `marc_flutter/lib/shared/ui/widgets/approval_gate.dart:120`
- Create: `marc_flutter/test/app/nav_shell_test.dart`

**Interfaces:**
- Consumes: `DashboardPage` (Task 8).
- Produces: route `/dashboard` sebagai branch shell indeks 0.

- [ ] **Step 1: Tulis ujian yang gagal**

Cipta `test/app/nav_shell_test.dart`:

```dart
// Urutan `destinations` dalam nav_shell.dart dan urutan `branches` dalam
// router.dart MESTI sepadan - go_router memilih branch mengikut INDEKS,
// jadi susunan yang tidak sepadan menghantar pengguna ke tab yang salah
// tanpa sebarang ralat. Ujian ini ialah satu-satunya benda yang
// menangkapnya.
// Payload kosong-tetapi-sah untuk setiap endpoint yang mana-mana tab
// mungkin panggil semasa ia dibina. Suis pada laluan, bukan satu jawapan
// untuk semua - tab yang menerima bentuk salah akan melontar semasa
// build dan menyembunyikan kegagalan navigasi sebenar.
ResponseBody _jawab(RequestOptions options) {
  final body = switch (options.path) {
    '/dashboard' => {
      'member': {
        'membership': {'status': 'approved'},
        'upcoming_registrations': [],
        'open_activities': [],
        'unread_notifications': 0,
        'certificates_total': 0,
        'total_members': 0,
      },
      'admin': null,
    },
    '/posts' => {'posts': [], 'next_cursor': null},
    '/activities' => {'activities': [], 'next_cursor': null},
    '/notifications' => {'notifications': [], 'next_cursor': null},
    _ => <String, dynamic>{},
  };
  return ResponseBody.fromString(
    jsonEncode(body),
    200,
    headers: {
      Headers.contentTypeHeader: [Headers.jsonContentType],
    },
  );
}

// Ujian ini memasang routerProvider yang SEBENAR (bukan router ujian
// seperti dalam dashboard_page_test.dart) - yang diuji ialah kandungan
// router itu sendiri, jadi menggantikannya akan menguji ketiadaan.
Future<GoRouter> _pumpApp(WidgetTester tester) async {
  final dio = Dio()..httpClientAdapter = _FakeAdapter((o) async => _jawab(o));
  late GoRouter router;
  await tester.pumpWidget(
    ProviderScope(
      overrides: [
        dioProvider.overrideWithValue(dio),
        authNotifierProvider.overrideWith((ref) => _LoggedInAuthNotifier()),
        myProfileProvider.overrideWith((ref) async => approvedProfile),
      ],
      child: Consumer(
        builder: (context, ref, _) {
          router = ref.watch(routerProvider);
          return MaterialApp.router(theme: AppTheme.light, routerConfig: router);
        },
      ),
    ),
  );
  await tester.pumpAndSettle();
  return router;
}

void main() {
  testWidgets('mengetuk setiap destinasi mendarat pada route yang sepadan', (tester) async {
    final router = await _pumpApp(tester);

    const dijangka = <(String, String)>[
      ('Utama', '/dashboard'),
      ('Hebahan', '/feed'),
      ('Aktiviti', '/activities'),
      ('Notifikasi', '/notifications'),
      ('Profil', '/profile'),
    ];

    for (final (label, laluan) in dijangka) {
      await tester.tap(find.text(label));
      await tester.pumpAndSettle();
      expect(
        router.state.matchedLocation,
        laluan,
        reason: 'tab "$label" sepatutnya membuka $laluan - '
            'urutan branches dalam router.dart tidak sepadan dengan '
            'urutan destinations dalam nav_shell.dart',
      );
    }
  });

  testWidgets('log masuk mendarat pada /dashboard, bukan /feed', (tester) async {
    final router = await _pumpApp(tester);

    // routerProvider bermula pada /login; refresh redirect untuk pengguna
    // yang sudah log masuk menyelesaikan ke destinasi lalai.
    expect(router.state.matchedLocation, '/dashboard');
  });
}
```

`_FakeAdapter` dan `_LoggedInAuthNotifier` disalin daripada
`notifications_page_test.dart:17-36`.
`approvedProfile` datang daripada `test/support/profile_fixtures.dart`
(Task 7).

Kalau `router.state` bukan API yang betul untuk versi go_router dalam
`pubspec.lock`, guna `router.routerDelegate.currentConfiguration.uri.path`
sebagai ganti — semak versi dahulu, jangan tukar-tukar sampai jadi.

- [ ] **Step 2: Jalankan ujian, sahkan ia GAGAL**

Run: `flutter test test/app/nav_shell_test.dart`
Expected: GAGAL — mendarat pada `/feed` untuk indeks 0.

- [ ] **Step 3: Kemas kini nav shell**

Dalam `nav_shell.dart`, ganti senarai `destinations` dengan lima:

```dart
        destinations: const [
          NavigationDestination(
            icon: Icon(Icons.home_outlined),
            selectedIcon: Icon(Icons.home),
            label: 'Utama',
          ),
          NavigationDestination(
            icon: Icon(Icons.campaign_outlined),
            selectedIcon: Icon(Icons.campaign),
            label: 'Hebahan',
          ),
          NavigationDestination(
            icon: Icon(Icons.event_outlined),
            selectedIcon: Icon(Icons.event),
            label: 'Aktiviti',
          ),
          NavigationDestination(
            icon: Icon(Icons.notifications_outlined),
            selectedIcon: Icon(Icons.notifications),
            label: 'Notifikasi',
          ),
          NavigationDestination(
            icon: Icon(Icons.person_outline),
            selectedIcon: Icon(Icons.person),
            label: 'Profil',
          ),
        ],
```

Tambah komen di atas senarai:

```dart
        // Urutan MESTI sepadan dengan urutan `branches` dalam
        // router.dart - goBranch memilih mengikut indeks, bukan nama.
        // Diuji oleh test/app/nav_shell_test.dart.
```

- [ ] **Step 4: Kemas kini router**

Dalam `router.dart`:

```dart
      StatefulShellRoute.indexedStack(
        builder: (_, _, shell) => NavShell(shell: shell),
        branches: [
          StatefulShellBranch(
            routes: [
              GoRoute(path: '/dashboard', builder: (_, _) => const DashboardPage()),
            ],
          ),
          StatefulShellBranch(
            routes: [
              GoRoute(path: '/feed', builder: (_, _) => const FeedPage()),
            ],
          ),
          // … tiga branch sedia ada, tidak berubah …
```

Tukar `'/feed'` → `'/dashboard'` pada **baris 78** (redirect selepas log masuk) dan **baris 142** (fallback `/checkout` bila `extra` hilang). Tambah import `DashboardPage`. Route `/feed` **kekal** — jangan buang.

- [ ] **Step 5: Kemas kini approval gate**

Dalam `approval_gate.dart:120`, tukar `context.go('/feed')` → `context.go('/dashboard')`. Kemas kini juga komen kepala fail (baris 7-19) yang menyebut "Feed kekal tempat kanonik urusan pendaftaran/bayaran" — kini itu Dashboard.

- [ ] **Step 6: Jalankan ujian, sahkan ia LULUS**

Run: `flutter test test/app/nav_shell_test.dart`
Expected: PASS untuk kedua-dua ujian.

- [ ] **Step 7: Jalankan suite penuh**

Run: `flutter analyze && flutter test`
Expected: analisis bersih, semua ujian lulus. Beri perhatian khusus kepada `test/features/posts/post_card_test.dart` dan `test/features/notifications/notifications_page_test.dart` — kedua-duanya menyebut `/feed`; kedua-duanya mendaftar route sendiri dan **sepatutnya** kekal lulus, tetapi sahkan, jangan andaikan.

- [ ] **Step 8: Jalankan app sebenar**

Run: `flutter run` (peranti atau emulator)
Sahkan dengan mata sendiri: log masuk mendarat pada Dashboard; lima tab kelihatan dan tidak bertindih pada telefon sempit; tab Hebahan memaparkan feed seperti dahulu; ahli pending (uji dengan akaun pending) melihat `PendingStatusView` pada tab Utama dengan butang bayar berfungsi.

- [ ] **Step 9: Stage**

```bash
git add lib/app/nav_shell.dart lib/app/router.dart \
        lib/shared/ui/widgets/approval_gate.dart \
        test/app/nav_shell_test.dart
```

---

## Ringkasan kebergantungan

```
Task 1 (endpoint asas)
  ├─ Task 2 (senarai aktiviti)
  ├─ Task 3 (blok admin) ── Task 4 (kutipan + derma)
  └─ Task 5 (yuran tertunggak)  ✔ prasyarat Staff ID sudah mendarat

Task 6 (model)
  └─ Task 7 (provider + ekstrak widget emel)
       └─ Task 8 (halaman + kad ahli)
            └─ Task 9 (blok admin UI)
                 └─ Task 10 (navigasi)  ← app berubah untuk pengguna DI SINI
```

Bahagian A dan B boleh dijalankan selari. Task 10 ialah task pertama yang
mengubah apa yang pengguna sedia ada lihat — sehingga ia mendarat,
segala-galanya sebelumnya ialah kod baharu yang tidak dirujuk sesiapa.

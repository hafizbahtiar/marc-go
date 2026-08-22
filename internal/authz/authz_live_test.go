package authz

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"marc/internal/db"
	"marc/internal/db/sqlc"
)

// `authz` ialah KESELURUHAN lapisan kebenaran app ni — gantian app-level
// bagi Postgres RLS yang belum wujud (Stage 9). Sebelum ni ia `no test
// files` (TODO.md L36), dan ujian live yang MEMANG menegaskan
// 403-untuk-bukan-management semuanya SKIP dalam CI (L14) — jadi tiada
// apa-apa dalam saluran automatik yang membuktikan kebenaran masih
// berkuat kuasa.
//
// Perlukan Postgres kerana hierarki rank dibaca daripada jadual `roles`
// yang di-seed oleh migration — memalsukannya bermakna menguji seed
// palsu, bukan yang sebenarnya akan dijalankan produksi:
//
//	AUTHZ_TEST_DB="postgres://localhost:5432/marc_authz_check?sslmode=disable" \
//	  go test ./internal/authz/ -v
func setup(t *testing.T) (*pgxpool.Pool, *sqlc.Queries, context.Context) {
	t.Helper()
	dbURL := os.Getenv("AUTHZ_TEST_DB")
	if dbURL == "" {
		t.Skip("set AUTHZ_TEST_DB kepada DB buangan")
	}
	if err := db.Migrate(dbURL); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool, sqlc.New(pool), ctx
}

// seedUserWithRole cipta user + profil dengan role yang diminta, dan
// pulangkan user id.
func seedUserWithRole(t *testing.T, ctx context.Context, pool *pgxpool.Pool, roleKey string) uuid.UUID {
	t.Helper()
	var userID uuid.UUID
	if err := pool.QueryRow(ctx,
		`insert into users (email, password_hash) values ($1, 'x') returning id`,
		"authz-"+uuid.NewString()+"@test.local").Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`insert into profiles (user_id, member_id, role_id)
		 values ($1, $2, (select id from roles where key = $3))`,
		userID, "AZ/"+uuid.NewString()[:12], roleKey); err != nil {
		t.Fatalf("seed profile (role=%s): %v", roleKey, err)
	}
	return userID
}

// Peta kategori ikut role, dibaca daripada seed migration SEBENAR.
//
// `tester` ialah yang paling penting di sini: ia SENGAJA berkategori
// 'ahli' walaupun ia akaun khas, supaya setiap gate IsManagement sedia
// ada terus menolaknya tanpa perubahan kod. Kalau seseorang "membetulkan"
// kategorinya kepada sesuatu yang lain, akaun review Google Play/App
// Store akan senyap mendapat akses pengurusan.
func TestIsManagementIkutKategoriRole(t *testing.T) {
	pool, q, ctx := setup(t)

	for _, tc := range []struct {
		roleKey string
		mahu    bool
	}{
		{"tester", false},
		{"ahli", false},
		{"supervisor", true},
		{"manager", true},
		{"admin", true},
		{"superadmin", true},
	} {
		t.Run(tc.roleKey, func(t *testing.T) {
			userID := seedUserWithRole(t, ctx, pool, tc.roleKey)

			got, err := IsManagement(ctx, q, userID)
			if err != nil {
				t.Fatalf("IsManagement: %v", err)
			}
			if got != tc.mahu {
				t.Errorf("IsManagement(%s) = %v, mahu %v", tc.roleKey, got, tc.mahu)
			}
		})
	}
}

// User tanpa profil (mustahil melalui laluan daftar biasa, tapi mungkin
// melalui data separa/pemadaman manual) mesti pulang RALAT, bukan
// `false` senyap — pemanggil melayan ralat sebagai 500 dan bukan sebagai
// "tolak", jadi perbezaannya penting.
func TestIsManagementRalatBilaProfilTiada(t *testing.T) {
	pool, q, ctx := setup(t)

	var userID uuid.UUID
	if err := pool.QueryRow(ctx,
		`insert into users (email, password_hash) values ($1, 'x') returning id`,
		"authz-noprofile-"+uuid.NewString()+"@test.local").Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	if _, err := IsManagement(ctx, q, userID); err == nil {
		t.Fatal("IsManagement pulang nil error untuk user tanpa profil — " +
			"pemanggil tak dapat bezakan 'bukan management' drpd 'tak dapat semak'")
	}
}

// IsAtLeastRole ialah gate yang LEBIH ketat drpd IsManagement, dipakai
// di tiga tempat dengan dua siling berbeza:
//
//	"manager"    — kategori aktiviti (infrastruktur dikongsi)
//	"superadmin" — domain emel disekat, data derma /admin/payments
//
// Sempadan yang paling mudah pecah ialah `admin`(80): ia management, dan
// ia LEBIH TINGGI drpd manager — tapi ia MESTI gagal semakan superadmin.
// Komen migration seed menyatakan ini secara eksplisit; ujian ni yang
// menguatkuasakannya.
func TestIsAtLeastRoleSempadanHierarki(t *testing.T) {
	pool, q, ctx := setup(t)

	for _, tc := range []struct {
		nama    string
		roleKey string
		siling  string
		mahu    bool
	}{
		{"ahli bukan manager", "ahli", "manager", false},
		{"tester bukan manager", "tester", "manager", false},
		{"supervisor bawah manager", "supervisor", "manager", false},
		{"manager memenuhi manager", "manager", "manager", true},
		{"admin atas manager", "admin", "manager", true},
		{"superadmin atas manager", "superadmin", "manager", true},

		{"manager BUKAN superadmin", "manager", "superadmin", false},
		// Sempadan yang paling penting dalam ujian ni.
		{"admin BUKAN superadmin", "admin", "superadmin", false},
		{"superadmin memenuhi superadmin", "superadmin", "superadmin", true},
	} {
		t.Run(tc.nama, func(t *testing.T) {
			userID := seedUserWithRole(t, ctx, pool, tc.roleKey)

			got, err := IsAtLeastRole(ctx, q, userID, tc.siling)
			if err != nil {
				t.Fatalf("IsAtLeastRole: %v", err)
			}
			if got != tc.mahu {
				t.Errorf("IsAtLeastRole(%s, >=%s) = %v, mahu %v",
					tc.roleKey, tc.siling, got, tc.mahu)
			}
		})
	}
}

// Kunci role yang tak wujud mesti pulang RALAT, bukan `false` — dan
// khususnya bukan `true`. Kunci yang tersalah eja pada tapak panggilan
// (cth `"super_admin"`) patut gagal dengan kuat, bukan senyap menolak
// semua orang atau senyap membenarkan semua orang.
func TestIsAtLeastRoleRalatBilaRoleTakWujud(t *testing.T) {
	pool, q, ctx := setup(t)
	userID := seedUserWithRole(t, ctx, pool, "superadmin")

	got, err := IsAtLeastRole(ctx, q, userID, "role_yang_tak_pernah_wujud")
	if err == nil {
		t.Fatalf("IsAtLeastRole dengan kunci role tak sah pulang (%v, nil) — "+
			"kunci tersalah eja akan gagal SENYAP", got)
	}
	if got {
		t.Fatal("IsAtLeastRole pulang TRUE untuk kunci role tak sah")
	}
}

// Pemalar CategoryManagement mesti padan nilai yang BENAR-BENAR
// di-seed dalam jadual roles. Kalau salah satu berubah tanpa yang lain,
// setiap gate management senyap menolak semua orang.
func TestCategoryManagementPadanSeedDB(t *testing.T) {
	pool, _, ctx := setup(t)

	var n int
	if err := pool.QueryRow(ctx,
		`select count(*) from roles where category = $1`, CategoryManagement).Scan(&n); err != nil {
		t.Fatalf("query: %v", err)
	}
	if n == 0 {
		t.Fatalf("tiada baris roles berkategori %q — pemalar Go dan seed DB "+
			"dah terpesong, setiap gate management akan menolak semua orang",
			CategoryManagement)
	}
}

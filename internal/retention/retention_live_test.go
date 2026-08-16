package retention

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"marc/internal/db"
	"marc/internal/db/sqlc"
)

// Lawan Postgres sebenar: keseluruhan reka bentuk bergantung pada trigger
// append-only membenarkan redaksi TAPI menolak segala-gala yang lain, dan
// itu tak boleh diuji tanpa enjin sebenar yang menjalankan trigger tu.
//
//	RETENTION_TEST_DB="postgres://localhost:5432/marc_retention_check?sslmode=disable" \
//	  go test ./internal/retention/ -v
func setup(t *testing.T) (*pgxpool.Pool, *sqlc.Queries, context.Context) {
	t.Helper()
	dbURL := os.Getenv("RETENTION_TEST_DB")
	if dbURL == "" {
		t.Skip("set RETENTION_TEST_DB kepada DB buangan")
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

func seedAuditLog(t *testing.T, ctx context.Context, pool *pgxpool.Pool, age time.Duration) int64 {
	t.Helper()
	var id int64
	err := pool.QueryRow(ctx, `
		insert into audit_logs (entity_type, entity_id, action, changed_fields,
		                        old_values, new_values, ip_address, user_agent, created_at)
		values ('post', $1, 'update', '{content}', '{"content":"a"}', '{"content":"b"}',
		        '203.0.113.7', 'Dart/3.9 (dart:io)', now() - $2::interval)
		returning id`, uuid.New(), age.String()).Scan(&id)
	if err != nil {
		t.Fatalf("seed audit log: %v", err)
	}
	return id
}

func TestRedaksiBuangPIITapiKekalkanCatatan(t *testing.T) {
	pool, q, ctx := setup(t)

	oldID := seedAuditLog(t, ctx, pool, 120*24*time.Hour) // 120 hari
	newID := seedAuditLog(t, ctx, pool, 10*24*time.Hour)  // 10 hari

	New(q, Policy{AuditPII: 90 * 24 * time.Hour}, time.Hour).RunOnce(ctx)

	var ip, ua *string
	var action, content string
	err := pool.QueryRow(ctx,
		`select ip_address, user_agent, action, new_values->>'content' from audit_logs where id = $1`,
		oldID).Scan(&ip, &ua, &action, &content)
	if err != nil {
		t.Fatalf("baca catatan lama: %v", err)
	}
	if ip != nil || ua != nil {
		t.Errorf("PII masih ada selepas redaksi: ip=%v ua=%v", ip, ua)
	}
	// Yang penting: catatan audit SENDIRI mesti selamat.
	if action != "update" || content != "b" {
		t.Errorf("redaksi merosakkan catatan: action=%q content=%q", action, content)
	}

	if err := pool.QueryRow(ctx,
		`select ip_address from audit_logs where id = $1`, newID).Scan(&ip); err != nil {
		t.Fatalf("baca catatan baharu: %v", err)
	}
	if ip == nil {
		t.Error("catatan yang belum tamat tempoh diredaksi awal")
	}
}

// Trigger mesti kekal menolak SEGALA update selain redaksi — kelonggaran
// untuk PDPA tak boleh sekali gus membuka pintu menulis semula sejarah.
func TestTriggerMasihTolakPengubahanSejarah(t *testing.T) {
	pool, _, ctx := setup(t)
	id := seedAuditLog(t, ctx, pool, time.Hour)

	cases := map[string]string{
		"tukar action":     `update audit_logs set action = 'create' where id = $1`,
		"tukar new_values": `update audit_logs set new_values = '{"content":"palsu"}' where id = $1`,
		"tukar actor":      `update audit_logs set actor_member_id = 'ORANG-LAIN' where id = $1`,
		"tukar created_at": `update audit_logs set created_at = now() - interval '5 years' where id = $1`,
		"tulis ganti ip":   `update audit_logs set ip_address = '198.51.100.1' where id = $1`,
	}
	for name, stmt := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := pool.Exec(ctx, stmt, id); err == nil {
				t.Fatal("UPDATE sepatutnya ditolak oleh trigger append-only")
			}
		})
	}

	// Kes yang pernah membuntukan: memadam user mencetuskan
	// `on delete set null` pada actor_id, iaitu satu UPDATE. Kalau trigger
	// menolaknya, akaun tak boleh dipadam LANGSUNG.
	t.Run("padam user boleh set actor_id NULL", func(t *testing.T) {
		var uid string
		if err := pool.QueryRow(ctx,
			`insert into users (email, password_hash) values ($1,'x') returning id`,
			"cascade-"+uuid.NewString()+"@test.local").Scan(&uid); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx,
			`insert into audit_logs (entity_type, entity_id, action, actor_id)
			 values ('profile', $1, 'update', $1)`, uid); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `delete from users where id = $1`, uid); err != nil {
			t.Fatalf("padam user disekat oleh trigger append-only: %v", err)
		}
		var n int
		if err := pool.QueryRow(ctx,
			`select count(*) from audit_logs where entity_id = $1 and actor_id is null`,
			uid).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("catatan audit patut kekal dgn actor_id NULL, dapat %d", n)
		}
	})

	// DELETE kekal dibenarkan (perlu untuk pruning simpanan).
	if _, err := pool.Exec(ctx, `delete from audit_logs where id = $1`, id); err != nil {
		t.Fatalf("DELETE patut dibenarkan untuk pruning: %v", err)
	}
}

func TestPadamCatatanAuditYangTamatTempoh(t *testing.T) {
	pool, q, ctx := setup(t)
	if _, err := pool.Exec(ctx, `delete from audit_logs`); err != nil {
		t.Fatalf("bersih: %v", err)
	}

	oldID := seedAuditLog(t, ctx, pool, 400*24*time.Hour)
	keepID := seedAuditLog(t, ctx, pool, 30*24*time.Hour)

	New(q, Policy{AuditRecord: 365 * 24 * time.Hour}, time.Hour).RunOnce(ctx)

	var n int
	if err := pool.QueryRow(ctx, `select count(*) from audit_logs where id = $1`, oldID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("catatan tamat tempoh masih ada")
	}
	if err := pool.QueryRow(ctx, `select count(*) from audit_logs where id = $1`, keepID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Error("catatan dalam tempoh dipadam")
	}
}

func TestPruneBatuNisanUpload(t *testing.T) {
	pool, q, ctx := setup(t)

	// Selesai & lama -> dibuang. Selesai & baharu -> kekal.
	// Masih tertunggak (deleted_at null) -> JANGAN SEKALI-KALI dibuang.
	for _, row := range []struct {
		key     string
		doneAgo string
	}{
		{"posts/_tomb-old.jpg", "40 days"},
		{"posts/_tomb-new.jpg", "2 days"},
	} {
		if _, err := pool.Exec(ctx,
			`insert into deleted_uploads (r2_key, reason, deleted_at)
			 values ($1, 'post_deleted', now() - $2::interval)
			 on conflict (r2_key) do update set deleted_at = excluded.deleted_at`,
			row.key, row.doneAgo); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	if _, err := pool.Exec(ctx,
		`insert into deleted_uploads (r2_key, reason, created_at)
		 values ('posts/_tomb-pending.jpg', 'post_deleted', now() - interval '90 days')
		 on conflict (r2_key) do nothing`); err != nil {
		t.Fatalf("seed pending: %v", err)
	}

	New(q, Policy{UploadTombstone: 30 * 24 * time.Hour}, time.Hour).RunOnce(ctx)

	for key, want := range map[string]int{
		"posts/_tomb-old.jpg":     0,
		"posts/_tomb-new.jpg":     1,
		"posts/_tomb-pending.jpg": 1, // belum dipadam dari R2 — mesti kekal
	} {
		var n int
		if err := pool.QueryRow(ctx,
			`select count(*) from deleted_uploads where r2_key = $1`, key).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != want {
			t.Errorf("%s: dapat %d baris, mahu %d", key, n, want)
		}
	}
}

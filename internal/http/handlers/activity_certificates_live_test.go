package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	"golang.org/x/time/rate"

	"marc/internal/audit"
	"marc/internal/db/sqlc"
	"marc/internal/http/middleware"
	"marc/internal/storage"
)

// ---- Helper seed ----

// sesiLepas — tetingkap sesi ke-i yang SUDAH tamat tetapi masih dalam
// tetingkap check-in (±CheckinWindowPadding).
//
// Kedua-dua syarat perlu serentak: penerbitan sijil hanya dibenarkan
// selepas sesi terakhir tamat, tetapi kehadiran hanya boleh ditanda dalam
// tetingkap check-in. Sesi yang diletakkan enam jam lalu memenuhi syarat
// pertama dan melanggar yang kedua.
func sesiLepas(i int) (start, end time.Time) {
	now := time.Now()
	offset := time.Duration(i) * 30 * time.Minute
	return now.Add(-100*time.Minute + offset), now.Add(-90*time.Minute + offset)
}

// seedAktivitiSelesai bina aktiviti yang sesi terakhirnya sudah tamat.
//
// Dibina atas seedActivityWithCapacity (Task 7) dan seedSession (Task 8):
// helper itu mencipta satu sesi masa hadapan, jadi sesi itu dibuang dan
// diganti dengan `bilSesi` sesi lepas, kemudian invarian starts_at/ends_at
// dikira semula melalui RecomputeActivityWindow — satu-satunya penulis sah
// tetingkap aktiviti.
//
// registration_closes_at yang ditetapkan seedActivityWithCapacity (now+24h)
// SENGAJA tidak diusik: registerTx menolak pendaftaran selepas tetingkap
// tutup, jadi ahli mesti masih boleh didaftarkan walaupun sesi sudah lepas.
func seedAktivitiSelesai(t *testing.T, pool *pgxpool.Pool, ambangPct, bilSesi int) (uuid.UUID, []uuid.UUID) {
	t.Helper()
	ctx := context.Background()

	activityID := seedActivityWithCapacity(t, pool, 50)

	// Sijil ialah `on delete restrict` atas activities — tanpa pembersihan
	// ini, cleanup seedActivityWithCapacity gagal secara senyap dan baris
	// ujian bertimbun dalam DB yang dikongsi. LIFO t.Cleanup menjamin ia
	// berjalan SEBELUM aktiviti dipadam.
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`delete from activity_certificates where activity_id = $1`, activityID)
	})

	if _, err := pool.Exec(ctx,
		`delete from activity_sessions where activity_id = $1`, activityID); err != nil {
		t.Fatalf("buang sesi lalai: %v", err)
	}

	sessions := make([]uuid.UUID, 0, bilSesi)
	for i := 0; i < bilSesi; i++ {
		start, end := sesiLepas(i)
		sessions = append(sessions, seedSession(t, pool, activityID, start, end))
	}

	if _, err := pool.Exec(ctx,
		`update activities set attendance_threshold_pct = $2 where id = $1`,
		activityID, ambangPct); err != nil {
		t.Fatalf("tetapkan ambang: %v", err)
	}
	if err := sqlc.New(pool).RecomputeActivityWindow(ctx, activityID); err != nil {
		t.Fatalf("kira semula tetingkap: %v", err)
	}
	return activityID, sessions
}

// hadirkan daftarkan seorang ahli dan tandakan kehadirannya pada `bil` sesi
// pertama, melalui registerTx/markAttendanceTx sebenar — bukan insert
// terus, supaya seed tidak boleh menghasilkan keadaan yang laluan sebenar
// tak boleh capai.
func hadirkan(t *testing.T, pool *pgxpool.Pool, activityID uuid.UUID, sessions []uuid.UUID, bil int) uuid.UUID {
	t.Helper()
	ctx := context.Background()

	userID := seedUsers(t, pool, 1)[0]
	reg, err := registerTx(ctx, pool, activityID, userID)
	if err != nil {
		t.Fatalf("daftar: %v", err)
	}
	for i := 0; i < bil; i++ {
		if _, err := markAttendanceTx(ctx, pool, sessions[i], reg.ID, "manual", audit.Actor{}, nil); err != nil {
			t.Fatalf("tanda kehadiran sesi %d: %v", i, err)
		}
	}
	return userID
}

// seedSelesaiDenganKehadiran — aktiviti selesai (satu sesi) dengan n ahli
// yang semuanya hadir penuh.
func seedSelesaiDenganKehadiran(t *testing.T, pool *pgxpool.Pool, n int) (uuid.UUID, []uuid.UUID) {
	t.Helper()
	activityID, sessions := seedAktivitiSelesai(t, pool, 100, 1)
	users := make([]uuid.UUID, 0, n)
	for i := 0; i < n; i++ {
		users = append(users, hadirkan(t, pool, activityID, sessions, 1))
	}
	return activityID, users
}

// seedAktivitiTigaSesi — aktiviti selesai dengan tiga sesi dan ambang yang
// ditentukan pemanggil. Sesi dipulangkan melalui seedSesiAktiviti supaya
// seedPesertaKehadiran boleh mencarinya semula.
func seedAktivitiTigaSesi(t *testing.T, pool *pgxpool.Pool, ambangPct int) uuid.UUID {
	t.Helper()
	activityID, _ := seedAktivitiSelesai(t, pool, ambangPct, 3)
	return activityID
}

// sesiAktiviti baca id sesi mengikut seq — seedAktivitiTigaSesi hanya
// memulangkan id aktiviti (bentuk yang dipakai ujian), jadi peserta dicari
// sesinya di sini.
func sesiAktiviti(t *testing.T, pool *pgxpool.Pool, activityID uuid.UUID) []uuid.UUID {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`select id from activity_sessions where activity_id = $1 order by seq`, activityID)
	if err != nil {
		t.Fatalf("baca sesi: %v", err)
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan sesi: %v", err)
		}
		out = append(out, id)
	}
	return out
}

// seedPesertaKehadiran daftarkan tiga ahli dengan bilangan kehadiran
// berlainan — bentuk yang diperlukan untuk menguji ambang.
func seedPesertaKehadiran(
	t *testing.T, pool *pgxpool.Pool, activityID uuid.UUID, hadirA, hadirB, hadirC int,
) (a, b, c uuid.UUID) {
	t.Helper()
	sessions := sesiAktiviti(t, pool, activityID)
	return hadirkan(t, pool, activityID, sessions, hadirA),
		hadirkan(t, pool, activityID, sessions, hadirB),
		hadirkan(t, pool, activityID, sessions, hadirC)
}

// bacaSequence baca nilai semasa satu kaunter `sequences`. Kunci yang belum
// wujud ialah 0 — NextSequence pertama akan memulangkan 1.
func bacaSequence(t *testing.T, pool *pgxpool.Pool, key string) int64 {
	t.Helper()
	var v int64
	err := pool.QueryRow(context.Background(),
		`select coalesce((select current_value from sequences where key = $1), 0)`, key).Scan(&v)
	if err != nil {
		t.Fatalf("baca sequence %s: %v", key, err)
	}
	return v
}

// ---- Fasa 1 ----

func TestTerbitSijilIdempoten(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	activityID, _ := seedSelesaiDenganKehadiran(t, pool, 3) // 3 ahli, semua hadir penuh

	first, err := issueCertificatesTx(ctx, pool, activityID, audit.Actor{})
	if err != nil {
		t.Fatalf("terbit pertama: %v", err)
	}
	if len(first) != 3 {
		t.Fatalf("sijil terbit = %d, mahu 3", len(first))
	}

	// Panggilan kedua tak boleh menerbitkan pendua ATAU membazirkan nombor
	// siri — unik (activity_id, user_id) menghalang baris, dan siri hanya
	// diambil untuk baris yang benar-benar dimasukkan.
	//
	// Kaunter dikepit di sekeliling panggilan kedua DENGAN SENGAJA. Tanpa
	// pengapit ini, versi yang melukis satu siri bagi setiap calon dan
	// memasukkan sifar baris tetap lulus ujian ini — dan `Issue` ialah
	// laluan menyambung yang direka untuk dipanggil berulang kali, jadi
	// setiap percubaan semula akan membakar satu blok nombor secara kekal.
	seqBefore := bacaSequence(t, pool, certificateSerialSequence)

	second, err := issueCertificatesTx(ctx, pool, activityID, audit.Actor{})
	if err != nil {
		t.Fatalf("terbit kedua: %v", err)
	}
	if len(second) != 0 {
		t.Errorf("terbit kedua hasilkan %d sijil, mahu 0", len(second))
	}
	if seqAfter := bacaSequence(t, pool, certificateSerialSequence); seqAfter != seqBefore {
		t.Errorf("sequence bergerak %d → %d pada terbit kedua yang tak hasilkan sijil",
			seqBefore, seqAfter)
	}

	q := sqlc.New(pool)
	all, _ := q.ListCertificatesByActivity(ctx, activityID)
	if len(all) != 3 {
		t.Errorf("jumlah sijil = %d, mahu 3", len(all))
	}

	// Satu catatan audit 'create' bagi setiap sijil, dan TIADA tambahan
	// daripada panggilan kedua.
	ids := make([]uuid.UUID, 0, len(first))
	for _, cert := range first {
		ids = append(ids, cert.ID)
	}
	var auditCount int
	if err := pool.QueryRow(ctx, `
		select count(*) from audit_logs
		where entity_type = $1 and action = 'create' and entity_id = any($2)`,
		audit.EntityCertificate, ids).Scan(&auditCount); err != nil {
		t.Fatalf("kira audit terbit: %v", err)
	}
	if auditCount != 3 {
		t.Errorf("catatan audit 'create' = %d, mahu 3", auditCount)
	}
}

func TestSijilHanyaUntukYangCukupAmbang(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	// 3 sesi, ambang 100%. Ahli A hadir 3, B hadir 2, C hadir 0.
	activityID := seedAktivitiTigaSesi(t, pool, 100)
	a, b, c := seedPesertaKehadiran(t, pool, activityID, 3, 2, 0)

	terbit, err := issueCertificatesTx(ctx, pool, activityID, audit.Actor{})
	if err != nil {
		t.Fatalf("terbit: %v", err)
	}
	if len(terbit) != 1 {
		t.Fatalf("sijil = %d, mahu 1 (hanya A)", len(terbit))
	}
	if terbit[0].UserID != a {
		t.Errorf("penerima = %v, mahu A (%v)", terbit[0].UserID, a)
	}
	_ = b
	_ = c
}

func TestFasaDuaMenyambungBarisTanpaR2Key(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	activityID, _ := seedSelesaiDenganKehadiran(t, pool, 2)
	if _, err := issueCertificatesTx(ctx, pool, activityID, audit.Actor{}); err != nil {
		t.Fatalf("terbit: %v", err)
	}

	q := sqlc.New(pool)
	pending, err := q.ListCertificatesPendingFile(ctx, activityID)
	if err != nil {
		t.Fatalf("ListCertificatesPendingFile: %v", err)
	}
	// Fasa 1 sengaja meninggalkan r2_key null — muat naik berlaku SELEPAS
	// komit, sebab rollback Postgres tidak boleh memadam objek R2.
	if len(pending) != 2 {
		t.Errorf("sijil menunggu fail = %d, mahu 2", len(pending))
	}
}

func TestNomborSiriTidakMelompatBilaTiadaSijil(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	before := bacaSequence(t, pool, certificateSerialSequence)

	// Aktiviti tanpa peserta layak → tiada siri diambil langsung.
	activityID := seedAktivitiTigaSesi(t, pool, 100)
	if _, err := issueCertificatesTx(ctx, pool, activityID, audit.Actor{}); err != nil {
		t.Fatalf("terbit: %v", err)
	}

	after := bacaSequence(t, pool, certificateSerialSequence)
	if after != before {
		t.Errorf("sequence bergerak %d → %d tanpa sijil diterbitkan", before, after)
	}
}

// TestNomborSiriBerundurBilaTransaksiGagal — inti sebab `sequences` dipakai
// dan bukan `create sequence` Postgres. Nombor siri diambil DALAM transaksi;
// kalau mana-mana penerima gagal, keseluruhan penerbitan berundur dan
// kaunter mesti kekal di tempat asalnya. Nextval Postgres tidak berundur.
func TestNomborSiriBerundurBilaTransaksiGagal(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	activityID, users := seedSelesaiDenganKehadiran(t, pool, 2)

	// Nama yang tidak boleh dicetak pada fon terbina PDF. ListEligible...
	// mengisih ikut display_name, jadi nama yang bermula dengan 'Z' datang
	// SELEPAS ahli tanpa nama paparan — sekurang-kurangnya satu siri sudah
	// diambil sebelum kegagalan berlaku.
	if _, err := pool.Exec(ctx,
		`update profiles set display_name = $2 where user_id = $1`,
		users[1], "Zulkifli 锦标赛"); err != nil {
		t.Fatalf("tetapkan nama: %v", err)
	}

	before := bacaSequence(t, pool, certificateSerialSequence)

	if _, err := issueCertificatesTx(ctx, pool, activityID, audit.Actor{}); err == nil {
		t.Fatal("terbit patut gagal atas nama yang tidak boleh dicetak")
	}

	if after := bacaSequence(t, pool, certificateSerialSequence); after != before {
		t.Errorf("sequence bergerak %d → %d walaupun transaksi berundur", before, after)
	}
	all, err := sqlc.New(pool).ListCertificatesByActivity(ctx, activityID)
	if err != nil {
		t.Fatalf("senarai sijil: %v", err)
	}
	if len(all) != 0 {
		t.Errorf("sijil tersimpan = %d, mahu 0 selepas rollback", len(all))
	}
}

// TestTajukTidakBolehDicetakDitolakSebelumBarisWujud — fasa 1 MENSNAPSHOT
// activity_title ke dalam baris sijil, dan fasa 2 membaca snapshot itu.
// Kalau tajuk yang rosak sempat masuk ke baris, membetulkan tajuk aktiviti
// melalui API tidak akan menyentuhnya dan setiap pusingan fasa 2 gagal
// selama-lamanya. Jadi ia mesti ditolak sebelum SEBARANG baris wujud.
func TestTajukTidakBolehDicetakDitolakSebelumBarisWujud(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	activityID, _ := seedSelesaiDenganKehadiran(t, pool, 1)
	if _, err := pool.Exec(ctx,
		`update activities set title = $2 where id = $1`, activityID, "Kejohanan 锦标赛"); err != nil {
		t.Fatalf("tetapkan tajuk: %v", err)
	}

	before := bacaSequence(t, pool, certificateSerialSequence)

	_, err := issueCertificatesTx(ctx, pool, activityID, audit.Actor{})
	if !errors.Is(err, errUnprintableCertificateField) {
		t.Fatalf("ralat = %v, mahu errUnprintableCertificateField", err)
	}
	if !strings.Contains(err.Error(), "ActivityTitle") {
		t.Errorf("mesej %q tidak menamakan medan yang menyinggung", err.Error())
	}

	all, err := sqlc.New(pool).ListCertificatesByActivity(ctx, activityID)
	if err != nil {
		t.Fatalf("senarai sijil: %v", err)
	}
	if len(all) != 0 {
		t.Errorf("sijil tersimpan = %d, mahu 0", len(all))
	}
	if after := bacaSequence(t, pool, certificateSerialSequence); after != before {
		t.Errorf("sequence bergerak %d → %d walaupun tiada sijil diterbitkan", before, after)
	}
}

func TestTerbitSijilSebelumAktivitiTamatDitolak(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	// seedActivityWithCapacity meletakkan sesinya 720 jam ke hadapan.
	activityID := seedActivityWithCapacity(t, pool, 10)

	if _, err := issueCertificatesTx(ctx, pool, activityID, audit.Actor{}); err != errActivityNotFinished {
		t.Fatalf("ralat = %v, mahu errActivityNotFinished", err)
	}
}

// TestTerbitSijilSerentakTidakMelompatkanNomborSiri — pasangan kepada
// TestRegisterPerlumbaanSlotTerakhir, untuk laluan penerbitan.
//
// issueCertificatesTx dahulunya membaca aktiviti, bilangan sesi, calon
// layak dan senarai "sudah bersijil" DI LUAR transaksi, dan hanya kemudian
// membuka `pool.Begin`. Dua pengurus yang menekan Terbitkan serentak
// mensnapshot `sudahBersijil` kosong pada kedua-dua belah, kedua-duanya
// melukis NextSequence bagi setiap calon, dan yang kalah jatuh ke
// `on conflict do nothing` → `continue` → dan TETAP komit — kaunter siri
// bergerak dua kali ganda dan lompang kekal dalam penomboran. Itulah bug
// yang jadual `sequences` (Task 9) dibina untuk menutupnya.
//
// Kunci baris aktiviti sebagai pernyataan PERTAMA dalam transaksi
// menyerikan kedua-dua penerbitan; yang kedua kini membaca senarai sijil
// SELEPAS yang pertama komit, jadi ia melangkau semua calon tanpa melukis
// satu nombor pun.
func TestTerbitSijilSerentakTidakMelompatkanNomborSiri(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	const bilAhli = 4
	activityID, _ := seedSelesaiDenganKehadiran(t, pool, bilAhli)

	// Panaskan pool DAHULU. pgxpool lalai MinConns = 0 dan mencipta
	// sambungan secara malas; beberapa milisaat untuk mewujudkan setiap
	// satu sudah cukup untuk menyerikan goroutine secara tak sengaja, dan
	// ujian ini akan LULUS walaupun kunci dibuang. Ditetapkan di Task 7 —
	// bukan pilihan.
	warmPool(t, pool, 2)

	seqSebelum := bacaSequence(t, pool, certificateSerialSequence)

	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		jumlah int
		hasil  []int
	)
	mula := make(chan struct{})
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-mula
			terbit, err := issueCertificatesTx(ctx, pool, activityID, audit.Actor{})
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				t.Errorf("terbit serentak: %v", err)
				return
			}
			jumlah += len(terbit)
			hasil = append(hasil, len(terbit))
		}()
	}
	close(mula)
	wg.Wait()

	// Kaunter siri ialah pengesan sebenar: tanpa kunci ia bergerak 2×bilAhli
	// walaupun hanya bilAhli baris wujud.
	seqSelepas := bacaSequence(t, pool, certificateSerialSequence)
	if delta := seqSelepas - seqSebelum; delta != bilAhli {
		t.Errorf("sequence bergerak %d, mahu tepat %d — nombor siri dibakar oleh penerbitan yang kalah",
			delta, bilAhli)
	}

	// Satu penerbitan mengambil semuanya, satu lagi tiada apa-apa.
	sort.Ints(hasil)
	if len(hasil) != 2 || hasil[0] != 0 || hasil[1] != bilAhli {
		t.Errorf("agihan sijil setiap panggilan = %v, mahu [0 %d]", hasil, bilAhli)
	}
	if jumlah != bilAhli {
		t.Errorf("jumlah sijil dipulangkan = %d, mahu %d", jumlah, bilAhli)
	}

	all, err := sqlc.New(pool).ListCertificatesByActivity(ctx, activityID)
	if err != nil {
		t.Fatalf("senarai sijil: %v", err)
	}
	if len(all) != bilAhli {
		t.Errorf("baris sijil dalam DB = %d, mahu %d", len(all), bilAhli)
	}
	// Nombor siri mesti berturutan tanpa lompang.
	siri := make([]string, 0, len(all))
	for _, cert := range all {
		siri = append(siri, cert.Serial)
	}
	sort.Strings(siri)
	for i := 1; i < len(siri); i++ {
		var prev, cur int
		if _, err := fmt.Sscanf(siri[i-1][len(siri[i-1])-6:], "%d", &prev); err != nil {
			t.Fatalf("hurai siri %q: %v", siri[i-1], err)
		}
		if _, err := fmt.Sscanf(siri[i][len(siri[i])-6:], "%d", &cur); err != nil {
			t.Fatalf("hurai siri %q: %v", siri[i], err)
		}
		if cur != prev+1 {
			t.Errorf("lompang siri: %s → %s", siri[i-1], siri[i])
		}
	}
}

// ---- Revoke ----

func TestTarikBalikSijilKekalkanBarisDanGilirkanFail(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	activityID, _ := seedSelesaiDenganKehadiran(t, pool, 1)
	certs, err := issueCertificatesTx(ctx, pool, activityID, audit.Actor{})
	if err != nil {
		t.Fatalf("terbit: %v", err)
	}
	cert := certs[0]

	// Fasa 2 tidak dijalankan di sini (tiada R2), jadi r2_key ditetapkan
	// terus — yang diuji ialah gilir pemadaman, bukan muat naik.
	key := "certificates/" + cert.ID.String() + ".pdf"
	if err := sqlc.New(pool).SetCertificateR2Key(ctx, sqlc.SetCertificateR2KeyParams{
		ID: cert.ID, R2Key: pgText(key),
	}); err != nil {
		t.Fatalf("tetapkan r2_key: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `delete from deleted_uploads where r2_key = $1`, key)
	})

	if err := revokeCertificateTx(ctx, pool, cert.ID, "salah nama", audit.Actor{}); err != nil {
		t.Fatalf("tarik balik: %v", err)
	}

	// Baris MESTI kekal: sijil yang ditarik balik perlu tetap mengesahkan
	// sebagai ditarik balik, bukan lenyap.
	after, err := sqlc.New(pool).GetCertificateByID(ctx, cert.ID)
	if err != nil {
		t.Fatalf("baca sijil selepas tarik balik: %v", err)
	}
	if !after.RevokedAt.Valid {
		t.Error("revoked_at masih null selepas tarik balik")
	}
	if after.RevokedReason.String != "salah nama" {
		t.Errorf("revoked_reason = %q", after.RevokedReason.String)
	}

	var reason string
	if err := pool.QueryRow(ctx,
		`select reason from deleted_uploads where r2_key = $1`, key).Scan(&reason); err != nil {
		t.Fatalf("r2_key tidak digilirkan untuk dipadam: %v", err)
	}
	if reason != reasonCertificateRevoked {
		t.Errorf("reason = %q, mahu %q", reason, reasonCertificateRevoked)
	}

	var auditCount int
	if err := pool.QueryRow(ctx,
		`select count(*) from audit_logs where entity_type = $1 and entity_id = $2 and action = 'delete'`,
		audit.EntityCertificate, cert.ID).Scan(&auditCount); err != nil {
		t.Fatalf("kira audit: %v", err)
	}
	if auditCount != 1 {
		t.Errorf("catatan audit tarik balik = %d, mahu 1", auditCount)
	}

	// Tarik balik kedua kali bukan ralat pelayan — ia konflik.
	if err := revokeCertificateTx(ctx, pool, cert.ID, "lagi", audit.Actor{}); err != errCertificateAlreadyRevoked {
		t.Errorf("tarik balik kedua = %v, mahu errCertificateAlreadyRevoked", err)
	}
}

// ---- Handler ----

func certificateHandlerCall(
	t *testing.T, pool *pgxpool.Pool, callerID uuid.UUID,
	method, target string, params gin.Params, body string,
	fn func(*CertificateHandler, *gin.Context),
) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(method, target, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = params
	c.Set("userID", callerID)

	fn(NewCertificateHandler(pool, storage.NewR2Client("", "", "", "", ""), testPushService(pool), "https://marc.test", ""), c)
	return rec
}

func TestTerbitSijilPerluPengurusan(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	activityID, _ := seedSelesaiDenganKehadiran(t, pool, 1)
	ahli := seedMember(t, ctx, pool, "ahli", "approved")

	rec := certificateHandlerCall(t, pool, ahli, http.MethodPost,
		"/activities/"+activityID.String()+"/certificates",
		gin.Params{{Key: "id", Value: activityID.String()}}, "{}",
		(*CertificateHandler).Issue)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, mahu 403 (badan: %s)", rec.Code, rec.Body.String())
	}
}

func TestTarikBalikSijilPerluPengurusan(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	activityID, _ := seedSelesaiDenganKehadiran(t, pool, 1)
	certs, err := issueCertificatesTx(ctx, pool, activityID, audit.Actor{})
	if err != nil {
		t.Fatalf("terbit: %v", err)
	}
	ahli := seedMember(t, ctx, pool, "ahli", "approved")

	rec := certificateHandlerCall(t, pool, ahli, http.MethodPost,
		"/certificates/"+certs[0].ID.String()+"/revoke",
		gin.Params{{Key: "id", Value: certs[0].ID.String()}}, `{"reason":"cuba-cuba"}`,
		(*CertificateHandler).Revoke)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, mahu 403 (badan: %s)", rec.Code, rec.Body.String())
	}
	// Gerbang mesti menghalang SEBELUM apa-apa perubahan — 403 yang datang
	// selepas baris ditarik balik tiada nilai.
	after, err := sqlc.New(pool).GetCertificateByID(ctx, certs[0].ID)
	if err != nil {
		t.Fatalf("baca sijil: %v", err)
	}
	if after.RevokedAt.Valid {
		t.Error("sijil ditarik balik walaupun pemanggil bukan pengurusan")
	}
}

func TestMuatTurunSijilBelumSiapPulang409(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	activityID, users := seedSelesaiDenganKehadiran(t, pool, 1)
	certs, err := issueCertificatesTx(ctx, pool, activityID, audit.Actor{})
	if err != nil {
		t.Fatalf("terbit: %v", err)
	}

	rec := certificateHandlerCall(t, pool, users[0], http.MethodGet,
		"/me/certificates/"+certs[0].ID.String()+"/file",
		gin.Params{{Key: "id", Value: certs[0].ID.String()}}, "",
		(*CertificateHandler).Download)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, mahu 409 (badan: %s)", rec.Code, rec.Body.String())
	}
}

func TestMuatTurunSijilOrangLainPulang404(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	activityID, _ := seedSelesaiDenganKehadiran(t, pool, 1)
	certs, err := issueCertificatesTx(ctx, pool, activityID, audit.Actor{})
	if err != nil {
		t.Fatalf("terbit: %v", err)
	}
	penceroboh := seedMember(t, ctx, pool, "ahli", "approved")

	rec := certificateHandlerCall(t, pool, penceroboh, http.MethodGet,
		"/me/certificates/"+certs[0].ID.String()+"/file",
		gin.Params{{Key: "id", Value: certs[0].ID.String()}}, "",
		(*CertificateHandler).Download)

	// 404, bukan 403: memberitahu penceroboh bahawa sijil itu WUJUD sudah
	// pun satu kebocoran.
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, mahu 404 (badan: %s)", rec.Code, rec.Body.String())
	}
}

func TestSenaraiSijilSayaHanyaMilikSendiri(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	activityID, users := seedSelesaiDenganKehadiran(t, pool, 2)
	if _, err := issueCertificatesTx(ctx, pool, activityID, audit.Actor{}); err != nil {
		t.Fatalf("terbit: %v", err)
	}

	rec := certificateHandlerCall(t, pool, users[0], http.MethodGet,
		"/me/certificates", nil, "", (*CertificateHandler).ListMine)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, mahu 200 (badan: %s)", rec.Code, rec.Body.String())
	}

	var body struct {
		Certificates []certificateResponse `json:"certificates"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("nyahkod badan: %v", err)
	}
	if len(body.Certificates) != 1 {
		t.Fatalf("sijil = %d, mahu 1", len(body.Certificates))
	}
	if body.Certificates[0].FileReady {
		t.Error("file_ready = true walaupun r2_key masih null")
	}
}

// ---- Pengesahan awam ----

// verifyRouter bina enjin gin SEBENAR dengan route awam dan baldi had kadar
// bernama yang sama seperti router.go.
//
// Melalui enjin, bukan memanggil handler terus: pemilihan medan DAN
// middleware had kadar kedua-duanya duduk pada lapisan HTTP, dan ujian yang
// memintasnya tidak membuktikan apa-apa tentang apa yang benar-benar
// diterima internet.
func verifyRouter(pool *pgxpool.Pool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewCertificateHandler(pool, storage.NewR2Client("", "", "", "", ""), testPushService(pool), "https://marc.test", "")
	r.GET(VerifyCertificateRoute,
		middleware.NewRateLimiter(nil).Limit(VerifyRateLimitBucket, rate.Every(2*time.Second), 20),
		h.Verify)
	return r
}

func doVerify(t *testing.T, pool *pgxpool.Pool, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	verifyRouter(pool).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func doGET(t *testing.T, pool *pgxpool.Pool, path string) []byte {
	t.Helper()
	rec := doVerify(t, pool, path)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, mahu 200 (badan: %s)", rec.Code, rec.Body.String())
	}
	return rec.Body.Bytes()
}

// Endpoint AWAM pertama yang mendedahkan nama ahli. Ujian ini ditulis
// sebagai penegasan atas SET medan, bukan atas nilai — supaya sesiapa yang
// menambah medan pada masa depan memecahkan ujian ini dan terpaksa
// memikirkannya semula. Semakan privasi yang bergantung pada ingatan tidak
// bertahan.
func TestVerifyTidakMendedahkanPII(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	activityID, _ := seedSelesaiDenganKehadiran(t, pool, 1)
	certs, err := issueCertificatesTx(ctx, pool, activityID, audit.Actor{})
	if err != nil || len(certs) != 1 {
		t.Fatalf("terbit: %v, %d sijil", err, len(certs))
	}

	body := doGET(t, pool, "/verify/certificates/"+certs[0].VerifyToken)

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("nyahkod respons: %v", err)
	}

	dibenarkan := map[string]bool{
		"serial": true, "recipient_name": true, "activity_title": true,
		"activity_date": true, "issued_at": true, "status": true,
	}
	for key := range payload {
		if !dibenarkan[key] {
			t.Errorf("respons awam mengandungi medan tak dibenarkan %q — "+
				"semak semula sama ada ia patut awam sebelum meluaskan senarai", key)
		}
	}
	for _, wajib := range []string{"serial", "recipient_name", "status"} {
		if _, ok := payload[wajib]; !ok {
			t.Errorf("respons kehilangan medan wajib %q", wajib)
		}
	}
	if payload["status"] != "sah" {
		t.Errorf("status = %v, mahu \"sah\"", payload["status"])
	}
}

func TestVerifyTokenTidakDikenaliSentiasa404(t *testing.T) {
	pool := activityTestPool(t)

	// Token yang tidak wujud langsung.
	tiada := doVerify(t, pool, "/verify/certificates/tokenyangtakwujudlangsung")
	// Token bentuk sah tetapi tak dikenali.
	salah := doVerify(t, pool, "/verify/certificates/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	if tiada.Code != http.StatusNotFound || salah.Code != http.StatusNotFound {
		t.Errorf("status = %d dan %d, kedua-duanya mahu 404 — respons berbeza "+
			"menjadi oracle yang mengesahkan token mana yang pernah wujud",
			tiada.Code, salah.Code)
	}
	// Status yang sama tidak cukup: badan yang berbeza ialah oracle yang
	// sama baiknya.
	if tiada.Body.String() != salah.Body.String() {
		t.Errorf("badan berbeza: %q lawan %q", tiada.Body.String(), salah.Body.String())
	}
}

// TestVerifySijilDitarikBalikKekalBolehDisemak — pengecualian yang disengajakan.
// Sijil yang ditarik balik pulang 200 dengan status "ditarik_balik", bukan
// 404: sijil yang lenyap senyap-senyap lebih buruk bagi orang yang sedang
// menyemaknya daripada satu yang menyatakan ia telah ditarik.
func TestVerifySijilDitarikBalikKekalBolehDisemak(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	activityID, _ := seedSelesaiDenganKehadiran(t, pool, 1)
	certs, err := issueCertificatesTx(ctx, pool, activityID, audit.Actor{})
	if err != nil || len(certs) != 1 {
		t.Fatalf("terbit: %v, %d sijil", err, len(certs))
	}
	if err := revokeCertificateTx(ctx, pool, certs[0].ID, "salah nama", audit.Actor{}); err != nil {
		t.Fatalf("tarik balik: %v", err)
	}

	body := doGET(t, pool, "/verify/certificates/"+certs[0].VerifyToken)

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("nyahkod respons: %v", err)
	}
	if payload["status"] != "ditarik_balik" {
		t.Errorf("status = %v, mahu \"ditarik_balik\"", payload["status"])
	}
	if payload["serial"] != certs[0].Serial {
		t.Errorf("serial = %v, mahu %q", payload["serial"], certs[0].Serial)
	}
}

// ---- Fasa 2 (perlu R2 sebenar) ----

// TestFasaDuaMuatNaikSebenar ialah SATU-SATUNYA ujian yang menyentuh R2.
// Ia di-skip melainkan R2_LIVE_TEST=1 — pemisahan dua fasa itu sendiri yang
// membolehkan semua ujian di atas berjalan tanpa kredential objek storan.
func TestFasaDuaMuatNaikSebenar(t *testing.T) {
	if os.Getenv("R2_LIVE_TEST") != "1" {
		t.Skip("tetapkan R2_LIVE_TEST=1 untuk jalankan")
	}
	pool := activityTestPool(t)
	ctx := context.Background()
	_ = godotenv.Load("../../../.env")

	r2 := storage.NewR2Client(
		os.Getenv("R2_ACCOUNT_ID"),
		os.Getenv("R2_ACCESS_KEY_ID"),
		os.Getenv("R2_SECRET_ACCESS_KEY"),
		os.Getenv("R2_BUCKET_NAME"),
		os.Getenv("R2_PUBLIC_URL"),
	)
	if !r2.Enabled() {
		t.Fatal("kredential R2 tak lengkap dalam .env")
	}

	activityID, _ := seedSelesaiDenganKehadiran(t, pool, 2)
	certs, err := issueCertificatesTx(ctx, pool, activityID, audit.Actor{})
	if err != nil {
		t.Fatalf("terbit: %v", err)
	}
	for _, cert := range certs {
		key := certificateR2Key(cert.ID)
		t.Cleanup(func() { _ = r2.DeleteImage(context.Background(), key) })
	}

	if _, err := fillPendingCertificateFiles(ctx, pool, r2, "https://marc.test", "", activityID); err != nil {
		t.Fatalf("fasa 2: %v", err)
	}

	pending, err := sqlc.New(pool).ListCertificatesPendingFile(ctx, activityID)
	if err != nil {
		t.Fatalf("senarai menunggu: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("masih menunggu fail = %d, mahu 0", len(pending))
	}

	// Pusingan kedua tidak sepatutnya melakukan apa-apa — itu maksud
	// "boleh disambung semula". err == nil sahaja tak cukup: fasa 2
	// pulang awal bila gilir kosong, jadi keadaan selepasnya yang
	// disemak semula.
	if _, err := fillPendingCertificateFiles(ctx, pool, r2, "https://marc.test", "", activityID); err != nil {
		t.Fatalf("fasa 2 ulangan: %v", err)
	}
	pending, err = sqlc.New(pool).ListCertificatesPendingFile(ctx, activityID)
	if err != nil {
		t.Fatalf("senarai menunggu selepas ulangan: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("menunggu fail selepas ulangan = %d, mahu 0", len(pending))
	}

	resp, err := http.Get(r2.SignedURL(ctx, certificateR2Key(certs[0].ID)))
	if err != nil {
		t.Fatalf("ambil semula: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status ambil semula = %d, mahu 200", resp.StatusCode)
	}
	head := make([]byte, 5)
	// ReadFull, bukan Read: satu Read sah untuk memulangkan kurang daripada
	// 5 bait, dan ujian yang gagal atas pemecahan rangkaian ialah bunyi.
	if _, err := io.ReadFull(resp.Body, head); err != nil {
		t.Fatalf("baca badan: %v", err)
	}
	if string(head) != "%PDF-" {
		t.Errorf("objek bukan PDF: %q", head)
	}
}

// ---- Notifikasi sijil (Task 11b, Bahagian B & C) ----

// Pengurus yang turut menyertai aktiviti ialah penerima sijil seperti orang
// lain. notifyMembers melangkau pelaku (betul untuk activity_published dan
// activity_cancelled — pelaku yang menyebabkan peristiwa itu), tetapi
// certificate_ready mengenai artifak penerima SENDIRI, dan peralihan
// pending→siap berlaku sekali sahaja: melangkaunya di sini bermakna dia
// tidak akan pernah diberitahu, dan menjalankan semula penerbitan tidak
// boleh memulihkannya.
func TestNotifikasiSijilSampaiKepadaPengurusYangTurutMenerima(t *testing.T) {
	pool := activityTestPool(t)
	ctx := context.Background()

	activityID, sessions := seedAktivitiSelesai(t, pool, 100, 1)

	// Pengurus DAN peserta pada masa yang sama.
	manager := seedMember(t, ctx, pool, "manager", "approved")
	reg, err := registerTx(ctx, pool, activityID, manager)
	if err != nil {
		t.Fatalf("daftar pengurus: %v", err)
	}
	if _, err := markAttendanceTx(ctx, pool, sessions[0], reg.ID, "manual",
		audit.Actor{UserID: manager}, nil); err != nil {
		t.Fatalf("tanda kehadiran pengurus: %v", err)
	}
	// Seorang ahli biasa juga, supaya ujian membuktikan pelaku DITAMBAH
	// kepada senarai dan bukan menggantikannya.
	ahli := hadirkan(t, pool, activityID, sessions, 1)

	certs, err := issueCertificatesTx(ctx, pool, activityID, audit.Actor{UserID: manager})
	if err != nil {
		t.Fatalf("terbit: %v", err)
	}
	if len(certs) != 2 {
		t.Fatalf("sijil diterbitkan = %d, mahu 2", len(certs))
	}

	h := NewCertificateHandler(pool, storage.NewR2Client("", "", "", "", ""),
		testPushService(pool), "https://marc.test", "")
	h.notifyCertificateReady(certs, manager)

	sijilBagi := make(map[uuid.UUID]uuid.UUID, len(certs))
	for _, cert := range certs {
		sijilBagi[cert.UserID] = cert.ID
	}

	for nama, userID := range map[string]uuid.UUID{"pengurus": manager, "ahli": ahli} {
		gotActivity, gotCert, jumpa := notifikasiUntuk(t, pool, userID, "certificate_ready")
		if !jumpa {
			t.Fatalf("%s: tiada baris notifikasi certificate_ready", nama)
		}
		if gotActivity == nil || *gotActivity != activityID {
			t.Errorf("%s: activity_id = %v, mahu %v", nama, gotActivity, activityID)
		}
		// Setiap penerima dipautkan kepada sijilnya SENDIRI, bukan sijil
		// pertama dalam senarai.
		if gotCert == nil || *gotCert != sijilBagi[userID] {
			t.Errorf("%s: certificate_id = %v, mahu %v", nama, gotCert, sijilBagi[userID])
		}
	}
}

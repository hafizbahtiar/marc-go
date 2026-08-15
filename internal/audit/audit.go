// Package audit rekod jejak "siapa ubah apa, bila" untuk entiti yang
// boleh disunting.
//
// Reka bentuk:
//
//   - Satu jadual generik (audit_logs) untuk semua entiti — tambah entiti
//     baharu = tambah pemalar Entity, bukan migration baharu.
//   - Delta sahaja. Untuk 'update' cuma field yang BERUBAH disimpan; snapshot
//     penuh setiap suntingan akan menggandakan saiz jadual tanpa faedah.
//   - Dipanggil DALAM transaksi yang sama dengan mutasi (lihat Record).
//     Jejak audit "best-effort" yang boleh gagal senyap bukan jejak audit:
//     kes yang paling penting untuk direkod (ralat, penyalahgunaan) ialah
//     kes yang paling mungkin gagal ditulis.
package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"marc/internal/db/sqlc"
)

// Jenis entiti. Tambah di sini bila entiti baharu mula diaudit.
const (
	EntityPost     = "post"
	EntityComment  = "comment"
	EntityProfile  = "profile"
	EntityActivity = "activity"
	// Kehadiran diaudit walaupun pendaftaran tidak: baris ini yang
	// menentukan siapa layak menerima sijil.
	EntityAttendance = "activity_attendance"
	// Sijil diaudit pada penerbitan DAN penarikan balik: baris sijil kekal
	// selepas ditarik balik, tetapi sebab dan pelakunya hanya ada di sini.
	EntityCertificate = "activity_certificate"
	// Kategori aktiviti — infrastruktur dikongsi semua aktiviti (bukan
	// tindakan pengurusan harian), jadi diaudit sama macam role.
	EntityActivityCategory = "activity_category"
)

const (
	ActionCreate = "create"
	ActionUpdate = "update"
	ActionDelete = "delete"
)

// Actor — siapa buat perubahan. MemberID dan RoleKey disnapshot sebagai
// teks sebab role berubah dari masa ke masa: kita nak tahu kuasa yang dia
// ADA masa tindakan itu, bukan kuasa dia sekarang.
type Actor struct {
	UserID    uuid.UUID
	MemberID  string
	RoleKey   string
	IP        string
	UserAgent string
}

// Entry — satu catatan audit.
type Entry struct {
	EntityType string
	EntityID   uuid.UUID
	Action     string
	Actor      Actor

	// Old/New ialah perwakilan entiti sebagai map field. Untuk Update,
	// hantar kedua-duanya penuh — Record akan kira deltanya sendiri.
	// Create: Old nil. Delete: New nil.
	Old map[string]any
	New map[string]any
}

// Record tulis satu catatan audit.
//
// `q` MESTI Queries yang terikat pada transaksi mutasi (`queries.WithTx(tx)`),
// supaya catatan dan perubahan sebenar commit atau rollback bersama. Kalau
// gagal, caller patut gagalkan keseluruhan permintaan — jangan telan ralat.
func Record(ctx context.Context, q *sqlc.Queries, e Entry) error {
	oldDelta, newDelta, changed := Diff(e.Old, e.New)

	// Update yang tak ubah apa-apa (user tekan Simpan tanpa edit) — jangan
	// kotorkan jejak dengan baris kosong.
	if e.Action == ActionUpdate && len(changed) == 0 {
		return nil
	}

	oldJSON, err := marshalOrNil(oldDelta)
	if err != nil {
		return fmt.Errorf("encode old_values: %w", err)
	}
	newJSON, err := marshalOrNil(newDelta)
	if err != nil {
		return fmt.Errorf("encode new_values: %w", err)
	}

	return q.CreateAuditLog(ctx, sqlc.CreateAuditLogParams{
		EntityType:    e.EntityType,
		EntityID:      e.EntityID,
		Action:        e.Action,
		ActorID:       pgtype.UUID{Bytes: e.Actor.UserID, Valid: e.Actor.UserID != uuid.Nil},
		ActorMemberID: text(e.Actor.MemberID),
		ActorRoleKey:  text(e.Actor.RoleKey),
		ChangedFields: changed,
		OldValues:     oldJSON,
		NewValues:     newJSON,
		IpAddress:     text(e.Actor.IP),
		UserAgent:     text(truncate(e.Actor.UserAgent, 512)),
	})
}

// Diff pulangkan subset old/new yang BERBEZA sahaja, plus nama field yang
// berubah (diisih supaya output deterministik dan senang diuji).
//
// Bila salah satu sisi nil (create/delete), sisi yang ada dikekalkan
// sepenuhnya — tiada apa nak dibanding.
func Diff(old, new map[string]any) (oldDelta, newDelta map[string]any, changed []string) {
	switch {
	case old == nil && new == nil:
		return nil, nil, nil
	case old == nil:
		return nil, new, sortedKeys(new)
	case new == nil:
		return old, nil, sortedKeys(old)
	}

	oldDelta = map[string]any{}
	newDelta = map[string]any{}

	// Union kunci dua-dua belah: field yang dibuang atau ditambah pun
	// perubahan.
	seen := map[string]bool{}
	for _, key := range append(sortedKeys(old), sortedKeys(new)...) {
		if seen[key] {
			continue
		}
		seen[key] = true

		oldVal, newVal := old[key], new[key]
		if reflect.DeepEqual(oldVal, newVal) {
			continue
		}
		oldDelta[key] = oldVal
		newDelta[key] = newVal
		changed = append(changed, key)
	}

	sort.Strings(changed)
	if len(changed) == 0 {
		return nil, nil, nil
	}
	return oldDelta, newDelta, changed
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// marshalOrNil pulang nil (bukan "null" JSON) untuk map kosong, supaya
// lajur jsonb betul-betul NULL dan bukan literal null.
func marshalOrNil(m map[string]any) ([]byte, error) {
	if len(m) == 0 {
		return nil, nil
	}
	return json.Marshal(m)
}

func text(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}

// truncate elak User-Agent pelik/panjang membengkakkan jadual.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

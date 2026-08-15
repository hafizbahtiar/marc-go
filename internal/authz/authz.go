// Package authz gantikan Postgres RLS + is_management() Supabase dengan
// app-level check. Dua corak:
//
//  1. Role check (management vs ahli) — guna IsManagement dipanggil inline
//     dalam handler (tiada middleware generic kerana setiap check perlu
//     konteks yang unik).
//  2. Ownership check ("hanya resource sendiri") — TIADA fungsi generic
//     untuk ni. Handler mesti sentiasa scope query guna user id daripada
//     middleware.UserID(c) (hasil verify JWT), bukan daripada URL/body
//     yang client hantar. Ini setara RLS qual `auth.uid() = id` di
//     Supabase — kalau query tak filter guna id dari token, ownership
//     tak dikuatkuasakan.
package authz

import (
	"context"

	"github.com/google/uuid"

	"marc/internal/db/sqlc"
)

const CategoryManagement = "management"

// IsManagement semak sama ada user tergolong role kategori "management"
// (supervisor/manager/superadmin). Port dari Supabase is_management().
func IsManagement(ctx context.Context, q *sqlc.Queries, userID uuid.UUID) (bool, error) {
	category, err := q.GetRoleCategoryByUserID(ctx, userID)
	if err != nil {
		return false, err
	}
	return category == CategoryManagement, nil
}

// IsAtLeastRole semak sama ada rank role caller >= rank role `roleKey`.
// Lebih halus drpd IsManagement (cth "manager ke atas sahaja", exclude
// supervisor) — guna bila tindakan dikawal lebih ketat drpd management
// umum, cth kategori aktiviti (infrastruktur dikongsi semua aktiviti,
// bukan tindakan pengurusan harian biasa).
func IsAtLeastRole(ctx context.Context, q *sqlc.Queries, userID uuid.UUID, roleKey string) (bool, error) {
	profile, err := q.GetProfileByUserID(ctx, userID)
	if err != nil {
		return false, err
	}
	role, err := q.GetRoleByKey(ctx, roleKey)
	if err != nil {
		return false, err
	}
	return profile.RoleRank >= role.Rank, nil
}

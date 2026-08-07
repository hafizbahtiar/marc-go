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

# Stage 11 — Member Approval Status (MAIWP-only access)

Date: 2026-08-07
Status: approved for implementation

## Problem

MARC is restricted to MAIWP staff. Today, registration only gates on
`email_verified` — anyone who can receive email at the address they typed
gets full access once verified. There is no step where a human at MAIWP
confirms the registrant is actually staff. This spec adds that step.

## Data model

New columns on `profiles` (same table `email_verified` already lives on):

```sql
alter table profiles
  add column status text not null default 'pending'
    check (status in ('pending', 'approved', 'rejected')),
  add column approved_by uuid references users(id),
  add column approved_at timestamptz;
```

Migration backfills all existing rows to `status = 'approved'`
(`approved_by`/`approved_at` left null for backfilled rows — no real
approver to attribute). Only rows created after this migration default to
`pending`.

## Flow

1. `POST /auth/register` — unchanged except the new row implicitly gets
   `status = 'pending'` (DB default). No email sent at this point.
2. User can `POST /auth/login` immediately and receives a normal JWT.
   `GET /me` and `PATCH /me` work regardless of status (so the app can
   show a "menunggu kelulusan" screen and let the user fix a typo in
   their name/phone while waiting).
3. Every other protected endpoint (`/members`, `/device-tokens/*`,
   `/auth/verify-email/request`, `/auth/verify-email/confirm`, all of
   Posts/comments/notifications/uploads) returns 403 while
   `status != 'approved'`.
4. A management user calls `POST /members/:id/approve` →
   `status = 'approved'`, `approved_by`/`approved_at` set. From here the
   existing email-verification flow becomes reachable.
5. Once `email_verified` is also true (existing flow, unchanged), the
   user has full access — Posts etc. already gate on `email_verified` via
   `RequireVerifiedEmail`.
6. A management user can call `POST /members/:id/reject` instead →
   `status = 'rejected'`. The account row is kept (not deleted). The user
   can still log in and see their rejected status via `/me`, but
   everything else stays blocked.
7. Status transitions are not one-way: management can call `approve` or
   `reject` again later on any row (e.g. undo an accidental reject) —
   both endpoints simply set status + `approved_by`/`approved_at`
   (`approved_at` still records the most recent transition even for a
   reject, since it's really "last-decided-at").

## Middleware

New `RequireApprovedStatus` in `internal/http/middleware/verified.go`
(same file as `RequireVerifiedEmail`, same shape — reads
`profiles.status` for the JWT user, 403s with a Malay message if not
`approved`).

Applied in `router.go`:
- `protectedAuthGroup` (currently just `verify-email/request`) — add
  `RequireApprovedStatus` so email verification is unreachable pre-approval.
- A **new** group for `/members`, `/device-tokens/*` — currently part of
  the generic `protected` group alongside `/me`. `/me` (GET+PATCH) must
  stay exempt, so split `protected` into: `protected` (auth only, for
  `/me`) and a new `approved` group (`RequireAuth` +
  `RequireApprovedStatus`) for `/members` and `/device-tokens/*`.
- The existing `verified` group (Posts) already sits under `RequireAuth`
  + `RequireVerifiedEmail`; add `RequireApprovedStatus` there too so a
  rejected/pending user can't reach Posts even if `email_verified`
  somehow got set out of order (defense in depth, cheap to add).

## New endpoints

Management-only (reuse existing `authz.IsManagement`, same pattern as
`RequireManagement`/announcement posts):

- `GET /members?status=pending` — extend existing `Members` handler/query
  with an optional `status` filter (default: no filter / all, so existing
  callers are unaffected).
- `POST /members/:id/approve` — sets `status='approved'`,
  `approved_by=<jwt user>`, `approved_at=now()`.
- `POST /members/:id/reject` — sets `status='rejected'`,
  `approved_by=<jwt user>`, `approved_at=now()`.

Both 404 if `:id` doesn't resolve to a profile, 403 if caller isn't
management, 200 with the updated member row on success.

## Notifications

- **On register**: insert an in-app `notifications` row for every
  management user (batch insert, same shape as the `notifyOwner` helper
  from Stage 10 but fan-out to all management instead of one recipient —
  needs a new small helper, not a reuse of `notifyOwner` itself since
  that's single-recipient).
- **On approve/reject**: send a Resend email to the registrant (new
  email template, reuse `internal/email` client) AND insert one in-app
  `notifications` row for them.
- New `notifications.type` values: `member_pending` (→ management),
  `member_approved` / `member_rejected` (→ registrant).

## Out of scope (explicitly deferred)

- No new `admin` role — approval reuses `IsManagement`.
- No `suspended` status — only pending/approved/rejected for now.
- No bulk-approve UI/endpoint.
- Payment/dues gating — separate, already tracked as its own open item.

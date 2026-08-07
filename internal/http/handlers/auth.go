package handlers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"marc/internal/auth"
	"marc/internal/authz"
	"marc/internal/db/sqlc"
	"marc/internal/email"
	"marc/internal/http/middleware"
)

const emailVerificationTTL = time.Hour

// dummyPasswordHash — bcrypt hash tetap (bukan password sebenar
// sesiapa) dipakai untuk "bakar" masa bcrypt yang sama pada path
// email-tak-wujud di Login, elak timing oracle yang boleh bezakan
// "email wujud, password salah" (compare betul-betul jalan) dengan
// "email tak wujud" (return awal tanpa compare) — dua-dua patut ambil
// masa yang sama dari luar.
const dummyPasswordHash = "$2a$10$/8Dd.SDyfy2jxDvvxwPheeHLucYAitJ42OSSoz8wtyR1UTR8A3JfW"

// refreshReuseGraceWindow — replay token yang dah consumed DALAM tempoh
// ni dianggap race/retry biasa (concurrent request, network retry),
// BUKAN reuse attack — elak false-positive family revocation yang
// paksa re-login tanpa sebab. Attacker sebenar yang curi token dan guna
// lambat (lebih dari tempoh ni) tetap dikesan macam biasa.
const refreshReuseGraceWindow = 5 * time.Second

type AuthHandler struct {
	pool           *pgxpool.Pool
	queries        *sqlc.Queries
	jwt            *auth.JWT
	refreshTTL     time.Duration
	emailClient    *email.Client
	publicBaseURL  string
	emailVerifyURL string
}

func NewAuthHandler(
	pool *pgxpool.Pool,
	jwtSvc *auth.JWT,
	refreshTTL time.Duration,
	emailClient *email.Client,
	publicBaseURL string,
	emailVerifyURL string,
) *AuthHandler {
	return &AuthHandler{
		pool:           pool,
		queries:        sqlc.New(pool),
		jwt:            jwtSvc,
		refreshTTL:     refreshTTL,
		emailClient:    emailClient,
		publicBaseURL:  publicBaseURL,
		emailVerifyURL: emailVerifyURL,
	}
}

type tokenPairResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

// issueTokens generate access token + refresh token (rekod refresh token
// dalam DB). Access token TTL diambil dari j.accessTTL secara implicit
// melalui GenerateAccessToken.
func (h *AuthHandler) issueTokens(c *gin.Context, userID, familyID uuid.UUID) (tokenPairResponse, error) {
	access, err := h.jwt.GenerateAccessToken(userID)
	if err != nil {
		return tokenPairResponse{}, err
	}

	refresh, err := auth.GenerateOpaqueToken()
	if err != nil {
		return tokenPairResponse{}, err
	}

	_, err = h.queries.CreateRefreshToken(c.Request.Context(), sqlc.CreateRefreshTokenParams{
		UserID:    userID,
		TokenHash: auth.HashToken(refresh),
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(h.refreshTTL), Valid: true},
		FamilyID:  familyID,
	})
	if err != nil {
		return tokenPairResponse{}, err
	}

	return tokenPairResponse{
		AccessToken:  access,
		RefreshToken: refresh,
		ExpiresIn:    int(h.jwt.AccessTTL() / time.Second),
	}, nil
}

type registerRequest struct {
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required,min=6,max=72"`
}

func (h *AuthHandler) Register(c *gin.Context) {
	var req registerRequest
	if !bindJSON(c, &req) {
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))

	passwordHash, err := auth.HashPassword(req.Password)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal proses pendaftaran"})
		return
	}

	ctx := c.Request.Context()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal proses pendaftaran"})
		return
	}
	defer tx.Rollback(ctx)

	q := h.queries.WithTx(tx)

	user, err := q.CreateUser(ctx, sqlc.CreateUserParams{Email: req.Email, PasswordHash: passwordHash})
	if err != nil {
		if isUniqueViolation(err) {
			c.JSON(http.StatusConflict, gin.H{"error": "email ini sudah berdaftar"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal proses pendaftaran"})
		return
	}

	memberID, err := generateMemberID(ctx, q)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal proses pendaftaran"})
		return
	}

	role, err := q.GetRoleByKey(ctx, "ahli")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal proses pendaftaran"})
		return
	}

	if _, err := q.CreateProfile(ctx, sqlc.CreateProfileParams{
		UserID:   user.ID,
		MemberID: memberID,
		RoleID:   role.ID,
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal proses pendaftaran"})
		return
	}

	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal proses pendaftaran"})
		return
	}

	tokens, err := h.issueTokens(c, user.ID, uuid.New())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "pendaftaran berjaya tapi gagal log masuk, sila log masuk semula"})
		return
	}

	notifyManagementOfPendingMember(ctx, h.queries, user.ID)

	c.JSON(http.StatusCreated, tokens)
}

// notifyManagementOfPendingMember fan-out notification "ahli baru
// menunggu kelulusan" kepada semua management (Stage 11). Best-effort —
// kegagalan notification tak patut gagalkan pendaftaran yang dah
// berjaya (padanan pattern notifyOwner, Stage 10).
func notifyManagementOfPendingMember(ctx context.Context, q *sqlc.Queries, newUserID uuid.UUID) {
	managementIDs, err := q.ListManagementUserIDs(ctx, authz.CategoryManagement)
	if err != nil {
		log.Printf("gagal senarai management untuk notify ahli pending: %v", err)
		return
	}
	for _, recipientID := range managementIDs {
		if _, err := q.CreateNotification(ctx, sqlc.CreateNotificationParams{
			RecipientID: recipientID,
			ActorID:     newUserID,
			Type:        "member_pending",
			PostID:      pgtype.UUID{},
			CommentID:   pgtype.UUID{},
		}); err != nil {
			log.Printf("gagal cipta notification member_pending: %v", err)
		}
	}
}

// generateMemberID port dari Supabase `handle_new_user()`: format
// MARC{YYYY}/{MM}/{seq 4-digit}, ikut timezone Asia/Kuala_Lumpur.
func generateMemberID(ctx context.Context, q *sqlc.Queries) (string, error) {
	loc, err := time.LoadLocation("Asia/Kuala_Lumpur")
	if err != nil {
		loc = time.FixedZone("MYT", 8*60*60)
	}
	now := time.Now().In(loc)
	year := now.Format("2006")
	month := now.Format("01")

	seq, err := q.NextSequence(ctx, fmt.Sprintf("auth:%s:%s", year, month))
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("MARC%s/%s/%04d", year, month, seq), nil
}

type loginRequest struct {
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required,max=72"`
}

func (h *AuthHandler) Login(c *gin.Context) {
	var req loginRequest
	if !bindJSON(c, &req) {
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))

	ctx := c.Request.Context()
	user, err := h.queries.GetUserByEmail(ctx, req.Email)
	if err != nil {
		auth.VerifyPassword(dummyPasswordHash, req.Password)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "email atau kata laluan salah"})
		return
	}

	if !auth.VerifyPassword(user.PasswordHash, req.Password) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "email atau kata laluan salah"})
		return
	}

	tokens, err := h.issueTokens(c, user.ID, uuid.New())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "log masuk gagal"})
		return
	}

	c.JSON(http.StatusOK, tokens)
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token" binding:"required"`
}

func (h *AuthHandler) Refresh(c *gin.Context) {
	var req refreshRequest
	if !bindJSON(c, &req) {
		return
	}

	ctx := c.Request.Context()
	hash := auth.HashToken(req.RefreshToken)

	// Atomic single-use: UPDATE...RETURNING guard "consumed_at is null"
	// dalam SATU statement, sama race-safety macam DELETE...RETURNING
	// asal — kalau dua request serentak hantar hash yang sama, cuma
	// satu dapat row balik (menang); yang satu lagi dapat 0 rows.
	consumed, err := h.queries.ConsumeRefreshToken(ctx, hash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Sama ada token ni tak pernah wujud, ATAU dah consumed
			// sebelum ni. Kalau row wujud dan consumed_at dah set,
			// ini REUSE — tanda token dicuri (attacker consume dulu,
			// user asli cuba guna token yang sama lepas tu). Revoke
			// SEMUA token dalam family ni supaya chain attacker (dan
			// session user asli yang sama) sama-sama terputus, paksa
			// re-login penuh.
			if existing, ferr := h.queries.GetRefreshTokenByHash(ctx, hash); ferr == nil && existing.ConsumedAt.Valid {
				if time.Since(existing.ConsumedAt.Time) > refreshReuseGraceWindow {
					if rerr := h.queries.RevokeRefreshTokenFamily(ctx, existing.FamilyID); rerr != nil {
						log.Printf("gagal revoke refresh token family lepas reuse dikesan: %v", rerr)
					} else {
						log.Printf("refresh token reuse dikesan, family %s direvoke", existing.FamilyID)
					}
				}
			}
		}
		c.JSON(http.StatusUnauthorized, gin.H{"error": "refresh token tidak sah"})
		return
	}

	if consumed.ExpiresAt.Time.Before(time.Now()) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "refresh token sudah luput"})
		return
	}

	tokens, err := h.issueTokens(c, consumed.UserID, consumed.FamilyID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal reset sesi"})
		return
	}

	c.JSON(http.StatusOK, tokens)
}

func (h *AuthHandler) Logout(c *gin.Context) {
	var req refreshRequest
	if !bindJSON(c, &req) {
		return
	}

	// Idempotent — hapus je kalau wujud, tak kisah dah luput/tak wujud.
	_ = h.queries.DeleteRefreshTokenByHash(c.Request.Context(), auth.HashToken(req.RefreshToken))
	c.Status(http.StatusNoContent)
}

// LogoutAll padam SEMUA refresh token milik user semasa (semua device/
// session sekali gus) — "log keluar semua tempat". Berguna kalau akaun
// disyaki dikompromis atau device hilang, tanpa perlu tunggu setiap
// token luput sendiri.
func (h *AuthHandler) LogoutAll(c *gin.Context) {
	if err := h.queries.DeleteRefreshTokensByUser(c.Request.Context(), middleware.UserID(c)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal log keluar semua sesi"})
		return
	}
	c.Status(http.StatusNoContent)
}

// RequestEmailVerification jana token pengesahan dan hantar link
// pengesahan melalui email (Resend). Kalau `RESEND_API_KEY`/`EMAIL_FROM`
// belum diisi, `emailClient.Send` no-op senyap — token tetap dijana +
// disimpan, cuma di-log ke server supaya dev boleh test tanpa provider.
func (h *AuthHandler) RequestEmailVerification(c *gin.Context) {
	userID := middleware.UserID(c)
	ctx := c.Request.Context()

	if err := h.queries.DeleteEmailVerificationTokensByUser(ctx, userID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal jana token pengesahan"})
		return
	}

	token, err := auth.GenerateOpaqueToken()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal jana token pengesahan"})
		return
	}

	if _, err := h.queries.CreateEmailVerificationToken(ctx, sqlc.CreateEmailVerificationTokenParams{
		UserID:    userID,
		TokenHash: auth.HashToken(token),
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(emailVerificationTTL), Valid: true},
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal jana token pengesahan"})
		return
	}

	// Kalau EMAIL_VERIFY_URL configure (Stage 8, portfolio-astro), link
	// arah ke page branded tu. Kalau tidak, fallback ke Go punya HTML page
	// sendiri (GET /auth/verify-email/confirm) — dev/belum-setup portfolio.
	verifyPageBase := h.emailVerifyURL
	if verifyPageBase == "" {
		verifyPageBase = h.publicBaseURL + "/auth/verify-email/confirm"
	}
	link := fmt.Sprintf("%s?token=%s", verifyPageBase, token)

	if !h.emailClient.Enabled() {
		log.Printf("email verification (provider belum configure) untuk user %s: %s", userID, link)
	} else {
		user, err := h.queries.GetUserByID(ctx, userID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal jana token pengesahan"})
			return
		}
		html := fmt.Sprintf(
			`<p>Sahkan email anda untuk akaun MARC dengan klik pautan di bawah (luput dalam 1 jam):</p><p><a href="%s">%s</a></p>`,
			link, link,
		)
		if err := h.emailClient.Send(ctx, user.Email, "Sahkan Email MARC", html); err != nil {
			// Token dah disimpan; kegagalan hantar email tak patut sekat
			// user punya request (boleh cuba "Sahkan" lagi). Log je.
			log.Printf("gagal hantar email verification untuk user %s: %v", userID, err)
		}
	}

	c.Status(http.StatusNoContent)
}

// consumeEmailVerificationToken kongsi logic antara confirm via JSON
// body (app punya API call) dan confirm via GET link (klik terus dari
// email — tak perlu login/app dibuka).
func (h *AuthHandler) consumeEmailVerificationToken(ctx context.Context, token string) error {
	rec, err := h.queries.GetEmailVerificationTokenByHash(ctx, auth.HashToken(token))
	if err != nil {
		return errTokenInvalid
	}

	if rec.ExpiresAt.Time.Before(time.Now()) {
		_ = h.queries.DeleteEmailVerificationToken(ctx, rec.ID)
		return errTokenExpired
	}

	if err := h.queries.MarkEmailVerified(ctx, rec.UserID); err != nil {
		return err
	}

	_ = h.queries.DeleteEmailVerificationTokensByUser(ctx, rec.UserID)
	return nil
}

var (
	errTokenInvalid = errors.New("token tidak sah")
	errTokenExpired = errors.New("token sudah luput")
)

type confirmEmailVerificationRequest struct {
	Token string `json:"token" binding:"required"`
}

// ConfirmEmailVerification — dipanggil dari app (JSON body).
func (h *AuthHandler) ConfirmEmailVerification(c *gin.Context) {
	var req confirmEmailVerificationRequest
	if !bindJSON(c, &req) {
		return
	}

	if err := h.consumeEmailVerificationToken(c.Request.Context(), req.Token); err != nil {
		status := http.StatusBadRequest
		if !errors.Is(err, errTokenInvalid) && !errors.Is(err, errTokenExpired) {
			status = http.StatusInternalServerError
			err = errors.New("gagal sahkan email")
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}

	c.Status(http.StatusNoContent)
}

// ConfirmEmailVerificationLink — dipanggil terus dari pautan dalam
// email (klik, browser buka GET request). Render HTML ringkas, bukan
// JSON, sebab ni dibuka dalam browser bukan dipanggil app.
func (h *AuthHandler) ConfirmEmailVerificationLink(c *gin.Context) {
	token := c.Query("token")
	if token == "" {
		c.Data(http.StatusBadRequest, "text/html; charset=utf-8", []byte(verificationHTMLPage("Pautan tidak sah.")))
		return
	}

	if err := h.consumeEmailVerificationToken(c.Request.Context(), token); err != nil {
		c.Data(http.StatusBadRequest, "text/html; charset=utf-8", []byte(verificationHTMLPage(err.Error()+".")))
		return
	}

	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(verificationHTMLPage("Email anda berjaya disahkan. Boleh kembali ke app MARC.")))
}

func verificationHTMLPage(message string) string {
	return fmt.Sprintf(`<!doctype html>
<html lang="ms"><head><meta charset="utf-8"><title>Pengesahan Email MARC</title></head>
<body style="font-family: sans-serif; padding: 40px; text-align: center;">
<h2>MARC</h2>
<p>%s</p>
</body></html>`, message)
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

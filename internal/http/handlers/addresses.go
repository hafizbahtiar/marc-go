package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"marc/internal/db/sqlc"
	"marc/internal/http/middleware"
)

// maxAddressesPerUser — had produk (boleh berubah), disemak app-layer
// dalam transaksi sebelum INSERT, bukan constraint DB. Padanan cara
// sequences/nombor ahli dikira.
const maxAddressesPerUser = 3

// malaysianStates — 16 negeri/wilayah Malaysia. Senarai tertutup: state
// yang tak ada dalam senarai ni ditolak 400.
var malaysianStates = []string{
	"Johor", "Kedah", "Kelantan", "Melaka", "Negeri Sembilan", "Pahang",
	"Perak", "Perlis", "Pulau Pinang", "Sabah", "Sarawak", "Selangor",
	"Terengganu",
	"Wilayah Persekutuan Kuala Lumpur", "Wilayah Persekutuan Labuan",
	"Wilayah Persekutuan Putrajaya",
}

func isValidMalaysianState(s string) bool {
	for _, v := range malaysianStates {
		if v == s {
			return true
		}
	}
	return false
}

type addressResponse struct {
	ID          string  `json:"id"`
	Label       *string `json:"label"`
	IsDefault   bool    `json:"is_default"`
	AddressType string  `json:"address_type"`
	UnitNumber  *string `json:"unit_number"`
	Floor       *string `json:"floor"`
	Block       *string `json:"block"`
	Street      *string `json:"street"`
	Township    *string `json:"township"`
	City        string  `json:"city"`
	Postcode    string  `json:"postcode"`
	State       string  `json:"state"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

func toAddressResponse(a sqlc.MemberAddress) addressResponse {
	return addressResponse{
		ID:          a.ID.String(),
		Label:       textToPtr(a.Label),
		IsDefault:   a.IsDefault,
		AddressType: a.AddressType,
		UnitNumber:  textToPtr(a.UnitNumber),
		Floor:       textToPtr(a.Floor),
		Block:       textToPtr(a.Block),
		Street:      textToPtr(a.Street),
		Township:    textToPtr(a.Township),
		City:        a.City,
		Postcode:    a.Postcode,
		State:       a.State,
		CreatedAt:   formatTime(a.CreatedAt),
		UpdatedAt:   formatTime(a.UpdatedAt),
	}
}

// ListMyAddresses — GET /me/addresses. Self-service: ahli sendiri sahaja.
func (h *ProfileHandler) ListMyAddresses(c *gin.Context) {
	ctx := c.Request.Context()
	userID := middleware.UserID(c)

	rows, err := h.queries.ListAddressesByUser(ctx, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal muat senarai alamat"})
		return
	}

	res := make([]addressResponse, len(rows))
	for i, row := range rows {
		res[i] = toAddressResponse(row)
	}
	c.JSON(http.StatusOK, res)
}

type createAddressRequest struct {
	Label       *string `json:"label"`
	AddressType string  `json:"address_type" binding:"required"`
	UnitNumber  *string `json:"unit_number"`
	Floor       *string `json:"floor"`
	Block       *string `json:"block"`
	Street      *string `json:"street"`
	Township    *string `json:"township"`
	City        string  `json:"city" binding:"required"`
	Postcode    string  `json:"postcode" binding:"required"`
	State       string  `json:"state" binding:"required"`
	IsDefault   bool    `json:"is_default"`
}

// validateAddressType/Postcode/State — dikongsi Create dan Update.
func validAddressType(t string) bool {
	return t == "landed" || t == "highrise"
}

func validPostcode(p string) bool {
	if len(p) != 5 {
		return false
	}
	for _, r := range p {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// CreateAddress — POST /me/addresses. Alamat PERTAMA ahli paksa
// is_default=true tanpa kira body (elak keadaan "0 default"). Tolak 400
// kalau dah ada 3 (had disemak dalam transaksi yang sama, padanan
// corak sequences).
func (h *ProfileHandler) CreateAddress(c *gin.Context) {
	var req createAddressRequest
	if !bindJSON(c, &req) {
		return
	}

	if !validAddressType(req.AddressType) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "jenis alamat mesti 'landed' atau 'highrise'"})
		return
	}
	if !validPostcode(strings.TrimSpace(req.Postcode)) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "poskod mesti 5 digit"})
		return
	}
	if !isValidMalaysianState(strings.TrimSpace(req.State)) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "negeri tidak sah"})
		return
	}
	if len(strings.TrimSpace(req.City)) == 0 || len(req.City) > 100 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bandar diperlukan (maksimum 100 aksara)"})
		return
	}
	if req.Label != nil && len(*req.Label) > 100 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "label terlalu panjang (maksimum 100 aksara)"})
		return
	}

	ctx := c.Request.Context()
	userID := middleware.UserID(c)

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal cipta alamat"})
		return
	}
	defer tx.Rollback(ctx)
	q := h.queries.WithTx(tx)

	count, err := q.CountAddressesByUser(ctx, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal cipta alamat"})
		return
	}
	if count >= maxAddressesPerUser {
		c.JSON(http.StatusBadRequest, gin.H{"error": "had maksimum 3 alamat setiap ahli"})
		return
	}

	// Alamat PERTAMA — paksa default tanpa kira body, elak "0 default".
	isDefault := req.IsDefault
	if count == 0 {
		isDefault = true
	} else if isDefault {
		if err := q.UnsetDefaultForUser(ctx, userID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal cipta alamat"})
			return
		}
	}

	created, err := q.CreateAddress(ctx, sqlc.CreateAddressParams{
		UserID:      userID,
		Label:       ptrToPgText(req.Label),
		IsDefault:   isDefault,
		AddressType: req.AddressType,
		UnitNumber:  ptrToPgText(req.UnitNumber),
		Floor:       ptrToPgText(req.Floor),
		Block:       ptrToPgText(req.Block),
		Street:      ptrToPgText(req.Street),
		Township:    ptrToPgText(req.Township),
		City:        strings.TrimSpace(req.City),
		Postcode:    strings.TrimSpace(req.Postcode),
		State:       strings.TrimSpace(req.State),
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal cipta alamat"})
		return
	}

	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal cipta alamat"})
		return
	}

	c.JSON(http.StatusCreated, toAddressResponse(created))
}

type updateAddressRequest struct {
	Label       *string `json:"label"`
	AddressType *string `json:"address_type"`
	UnitNumber  *string `json:"unit_number"`
	Floor       *string `json:"floor"`
	Block       *string `json:"block"`
	Street      *string `json:"street"`
	Township    *string `json:"township"`
	City        *string `json:"city"`
	Postcode    *string `json:"postcode"`
	State       *string `json:"state"`
	IsDefault   *bool   `json:"is_default"`
}

// UpdateAddress — PATCH /me/addresses/:id. Semua medan pilihan (partial
// update, padanan PATCH /me). is_default=true nyahtetapkan default lama
// dalam transaksi yang sama. 404 kalau :id bukan milik caller.
func (h *ProfileHandler) UpdateAddress(c *gin.Context) {
	addressID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}

	var req updateAddressRequest
	if !bindJSON(c, &req) {
		return
	}

	if req.AddressType != nil && !validAddressType(*req.AddressType) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "jenis alamat mesti 'landed' atau 'highrise'"})
		return
	}
	if req.Postcode != nil && !validPostcode(strings.TrimSpace(*req.Postcode)) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "poskod mesti 5 digit"})
		return
	}
	if req.State != nil && !isValidMalaysianState(strings.TrimSpace(*req.State)) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "negeri tidak sah"})
		return
	}
	if req.City != nil && (len(strings.TrimSpace(*req.City)) == 0 || len(*req.City) > 100) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bandar diperlukan (maksimum 100 aksara)"})
		return
	}
	if req.Label != nil && len(*req.Label) > 100 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "label terlalu panjang (maksimum 100 aksara)"})
		return
	}

	ctx := c.Request.Context()
	userID := middleware.UserID(c)

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini alamat"})
		return
	}
	defer tx.Rollback(ctx)
	q := h.queries.WithTx(tx)

	if _, err := q.GetAddressByIDAndUser(ctx, sqlc.GetAddressByIDAndUserParams{ID: addressID, UserID: userID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "alamat tidak dijumpai"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini alamat"})
		return
	}

	updated, err := q.UpdateAddress(ctx, sqlc.UpdateAddressParams{
		ID:          addressID,
		UserID:      userID,
		Label:       ptrToPgText(req.Label),
		AddressType: ptrToPgText(req.AddressType),
		UnitNumber:  ptrToPgText(req.UnitNumber),
		Floor:       ptrToPgText(req.Floor),
		Block:       ptrToPgText(req.Block),
		Street:      ptrToPgText(req.Street),
		Township:    ptrToPgText(req.Township),
		City:        ptrToPgText(trimmedOrNil(req.City)),
		Postcode:    ptrToPgText(trimmedOrNil(req.Postcode)),
		State:       ptrToPgText(trimmedOrNil(req.State)),
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini alamat"})
		return
	}

	// is_default=true nyahtetapkan default LAMA dalam transaksi yang sama
	// (partial unique index tolak dua default serentak). is_default=false
	// sengaja diabaikan — spec tak sediakan cara "buang default tanpa
	// gantikan", dan invariant "wajib SATU default" mesti kekal.
	if req.IsDefault != nil && *req.IsDefault {
		if err := q.UnsetDefaultForUser(ctx, userID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini alamat"})
			return
		}
		updated, err = q.SetDefault(ctx, sqlc.SetDefaultParams{ID: addressID, UserID: userID})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini alamat"})
			return
		}
	}

	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal kemas kini alamat"})
		return
	}

	c.JSON(http.StatusOK, toAddressResponse(updated))
}

// DeleteAddress — DELETE /me/addresses/:id. Kalau baris dipadam ialah
// default DAN ada baris lain tinggal, auto-promote baris paling lama
// (created_at) jadi default baharu dalam transaksi yang sama.
func (h *ProfileHandler) DeleteAddress(c *gin.Context) {
	addressID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}

	ctx := c.Request.Context()
	userID := middleware.UserID(c)

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal padam alamat"})
		return
	}
	defer tx.Rollback(ctx)
	q := h.queries.WithTx(tx)

	target, err := q.GetAddressByIDAndUser(ctx, sqlc.GetAddressByIDAndUserParams{ID: addressID, UserID: userID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "alamat tidak dijumpai"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal padam alamat"})
		return
	}

	if err := q.DeleteAddress(ctx, sqlc.DeleteAddressParams{ID: addressID, UserID: userID}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal padam alamat"})
		return
	}

	if target.IsDefault {
		oldest, err := q.GetOldestOtherByUser(ctx, sqlc.GetOldestOtherByUserParams{UserID: userID, ID: addressID})
		if err == nil {
			if _, err := q.SetDefault(ctx, sqlc.SetDefaultParams{ID: oldest.ID, UserID: userID}); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal padam alamat"})
				return
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal padam alamat"})
			return
		}
	}

	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal padam alamat"})
		return
	}

	c.Status(http.StatusNoContent)
}

// ptrToPgText — *string -> pgtype.Text. nil = NULL (narg "tak dihantar",
// biar nilai asal). Berbeza drpd ptrToText(handlers/profile.go) yang
// terima `string` (bukan pointer) dan sentiasa Valid.
func ptrToPgText(s *string) pgtype.Text {
	if s == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: strings.TrimSpace(*s), Valid: true}
}

func trimmedOrNil(s *string) *string {
	if s == nil {
		return nil
	}
	v := strings.TrimSpace(*s)
	return &v
}

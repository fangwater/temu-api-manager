package httpapi

import (
	"net/http"
	"time"

	"temu-api-manager/internal/service"
)

func (s *Server) listSubstitutionOrders(w http.ResponseWriter, r *http.Request) {
	page, pageSize := pagination(r)
	ctx, cancel := s.context(r)
	defer cancel()
	items, total, substitutions, err := s.service.ListSubstitutionOrders(ctx, r.URL.Query().Get("q"), page, pageSize)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response{Success: true, Data: items, Meta: map[string]any{
		"page": page, "page_size": pageSize, "total": total,
		"active_substitutions": substitutions, "queried_at": time.Now(),
	}})
}

func (s *Server) compareSubstitutionPrices(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.context(r)
	defer cancel()
	item, err := s.service.CompareSubstitutionPrices(ctx, r.PathValue("parentOrderSN"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response{Success: true, Data: item})
}

func (s *Server) purchaseSubstitution(w http.ResponseWriter, r *http.Request) {
	var input struct {
		WarehouseKey string `json:"warehouse_key"`
		ChannelID    int64  `json:"channel_id,omitempty"`
		Confirm      bool   `json:"confirm"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if !input.Confirm {
		writeJSON(w, http.StatusBadRequest, response{Success: false, Error: "confirm=true is required"})
		return
	}
	ctx, cancel := s.context(r)
	defer cancel()
	item, err := s.service.PurchaseSubstitution(ctx, service.SubstitutionPurchaseRequest{
		ParentOrderSN: r.PathValue("parentOrderSN"), WarehouseKey: input.WarehouseKey,
		PreferredChannelID: input.ChannelID,
	})
	if err != nil {
		s.fail(w, err)
		return
	}
	status := http.StatusCreated
	if item.Duplicate {
		status = http.StatusOK
	}
	writeJSON(w, status, response{Success: true, Data: item})
}

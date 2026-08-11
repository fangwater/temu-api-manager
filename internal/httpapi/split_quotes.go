package httpapi

import (
	"net/http"

	"temu-api-manager/internal/service"
)

func (s *Server) prepareSplitPlan(w http.ResponseWriter, r *http.Request) {
	var input service.SplitPlanRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	ctx, cancel := s.context(r)
	defer cancel()
	item, err := s.service.PrepareSplitPlan(ctx, input)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response{Success: true, Data: item})
}

func (s *Server) quoteSplitPlan(w http.ResponseWriter, r *http.Request) {
	var input service.SplitQuoteRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	ctx, cancel := s.context(r)
	defer cancel()
	item, err := s.service.QuoteSplitPlan(ctx, input)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response{Success: true, Data: item})
}

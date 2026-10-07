package httpapi

import (
	"net/http"
)

func warehouseMappingsMoved(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusGone, response{Success: false, Error: "仓库配置已迁移至 XLWMS → 仓库管理 → 发货仓库"})
}
func (s *Server) legacyWarehouses(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.context(r)
	defer cancel()
	warehouses, mappings, err := s.service.LegacyWarehouses(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, response{Success: true, Data: map[string]any{"warehouses": warehouses, "mappings": mappings}})
}

func (s *Server) warehouseActivity(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.context(r)
	defer cancel()
	counts, err := s.service.WarehouseActivity(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, response{Success: true, Data: counts})
}

package httpapi

import (
	"net/http"
	"strings"
)

const shopHeader = "X-Temu-Shop"

type ShopInfo struct {
	Code    string `json:"code"`
	Name    string `json:"name"`
	Default bool   `json:"default"`
}

type ShopRouter struct {
	defaultCode string
	shops       []ShopInfo
	handlers    map[string]http.Handler
}

func NewShopRouter(defaultCode string, shops []ShopInfo, handlers map[string]http.Handler) http.Handler {
	return securityHeaders(&ShopRouter{defaultCode: defaultCode, shops: shops, handlers: handlers})
}

func (router *ShopRouter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/temu")
	if path == "/api/system/shops" {
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, response{Success: true, Data: map[string]any{
			"default_shop": router.defaultCode,
			"shops":        router.shops,
		}})
		return
	}
	code := strings.TrimSpace(r.Header.Get(shopHeader))
	if code == "" {
		code = router.defaultCode
	}
	handler, ok := router.handlers[code]
	if !ok {
		writeJSON(w, http.StatusBadRequest, response{Success: false, Error: "未知或未启用的店铺"})
		return
	}
	handler.ServeHTTP(w, r)
}

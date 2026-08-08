package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestShopRouterSelectsRequestedShop(t *testing.T) {
	handlers := map[string]http.Handler{
		"panda-homes": http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("homes")) }),
		"panda-buy":   http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("buy")) }),
	}
	router := NewShopRouter("panda-homes", []ShopInfo{{Code: "panda-homes"}, {Code: "panda-buy"}}, handlers)

	request := httptest.NewRequest(http.MethodGet, "/temu/api/orders", nil)
	request.Header.Set(shopHeader, "panda-buy")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Body.String() != "buy" {
		t.Fatalf("selected handler returned %q", response.Body.String())
	}
}

func TestShopRouterDefaultsAndListsShops(t *testing.T) {
	router := NewShopRouter("panda-homes", []ShopInfo{{Code: "panda-homes", Name: "PANDA HOMES", Default: true}}, map[string]http.Handler{
		"panda-homes": http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("homes")) }),
	})

	defaultResponse := httptest.NewRecorder()
	router.ServeHTTP(defaultResponse, httptest.NewRequest(http.MethodGet, "/temu/healthz", nil))
	if defaultResponse.Body.String() != "homes" {
		t.Fatalf("default handler returned %q", defaultResponse.Body.String())
	}

	shopsResponse := httptest.NewRecorder()
	router.ServeHTTP(shopsResponse, httptest.NewRequest(http.MethodGet, "/temu/api/system/shops", nil))
	if shopsResponse.Code != http.StatusOK || !strings.Contains(shopsResponse.Body.String(), "PANDA HOMES") {
		t.Fatalf("unexpected shops response: %d %s", shopsResponse.Code, shopsResponse.Body.String())
	}
}

func TestShopRouterRejectsUnknownShop(t *testing.T) {
	router := NewShopRouter("panda-homes", nil, map[string]http.Handler{"panda-homes": http.NotFoundHandler()})
	request := httptest.NewRequest(http.MethodGet, "/api/orders", nil)
	request.Header.Set(shopHeader, "missing")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("got status %d, want %d", response.Code, http.StatusBadRequest)
	}
}

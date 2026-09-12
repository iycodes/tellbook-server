package appdata

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

func TestTessaAccessForEveryProviderWithGlobalSwitch(t *testing.T) {
	h := &Handler{repo: &Repository{}}
	h.ConfigureTessa(true, "test-v1", "local", "test", tessaTestConfigHash, &TessaEventBroker{})
	for range 3 {
		if !h.tessaAvailable(uuid.New()) {
			t.Fatal("enabled Tessa must admit every authenticated provider, without account configuration")
		}
	}
	if h.tessaAvailable(uuid.Nil) {
		t.Fatal("an empty account ID is not a provider")
	}
	w := httptest.NewRecorder()
	if _, ok := h.tessaClient(w, httptest.NewRequest(http.MethodGet, "/v1/app/tessa", nil)); ok || w.Code != http.StatusUnauthorized {
		t.Fatal("Tessa must still reject unauthenticated requests", w.Code)
	}
	h.ConfigureTessa(false, "test-v1", "local", "test", tessaTestConfigHash, &TessaEventBroker{})
	if h.tessaAvailable(uuid.New()) {
		t.Fatal("global disable must prevent provider access")
	}
	h.ConfigureTessa(true, "test-v1", "local", "test", tessaTestConfigHash, nil)
	if h.tessaAvailable(uuid.New()) {
		t.Fatal("missing runtime dependencies must still prevent access")
	}
}

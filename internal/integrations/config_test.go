package integrations

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/google/uuid"
)

func integrationConfigEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("INTEGRATIONS_ENABLED", "true")
	t.Setenv("INTEGRATIONS_WRITES_ENABLED", "false")
	t.Setenv("INTEGRATIONS_PUBLIC_BASE_URL", "https://api.tellbook.test")
	t.Setenv("INTEGRATIONS_CHATGPT_CLIENT_METADATA_URL", "")
	t.Setenv("INTEGRATIONS_CHATGPT_REDIRECT_URI", "")
	t.Setenv("INTEGRATIONS_CLAUDE_CLIENT_METADATA_URL", "https://claude.ai/oauth/mcp-oauth-client-metadata")
}

func TestLoadIntegrationProviderSelection(t *testing.T) {
	first := uuid.MustParse("10000000-0000-4000-8000-000000000001")
	second := uuid.MustParse("10000000-0000-4000-8000-000000000002")
	for _, test := range []struct {
		name       string
		selection  string
		enabled    string
		all        bool
		providers  int
		allowFirst bool
		allowOther bool
		invalid    bool
	}{
		{"all", "all", "true", true, 0, true, true, false},
		{"normalized all", "  ALL  ", "true", true, 0, true, true, false},
		{"one provider", first.String(), "true", false, 1, true, false, false},
		{"two providers", first.String() + ", " + second.String(), "true", false, 2, true, true, false},
		{"disabled all", "all", "false", true, 0, false, false, false},
		{"disabled empty", "", "false", false, 0, false, false, false},
		{"enabled empty", "", "true", false, 0, false, false, true},
		{"invalid ID", "not-a-uuid", "true", false, 0, false, false, true},
		{"mixed all", "all," + first.String(), "true", false, 0, false, false, true},
		{"mixed trailing all", first.String() + ",ALL", "true", false, 0, false, false, true},
		{"repeated all", "all,all", "true", false, 0, false, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			integrationConfigEnvironment(t)
			t.Setenv("INTEGRATIONS_ENABLED", test.enabled)
			t.Setenv("INTEGRATIONS_PROVIDER_ALLOWLIST", test.selection)
			cfg, err := LoadConfig("https://app.tellbook.test")
			if (err != nil) != test.invalid {
				t.Fatalf("LoadConfig() error = %v, invalid = %v", err, test.invalid)
			}
			if test.invalid {
				return
			}
			if cfg.AllProviders != test.all || len(cfg.Providers) != test.providers {
				t.Fatal("Provider selection was not preserved")
			}
			s := &Service{cfg: cfg}
			if s.allowed(first) != test.allowFirst || s.allowed(second) != test.allowOther {
				t.Fatal("Provider access did not match the configured selection and enable switch")
			}
			if s.allowed(uuid.Nil) {
				t.Fatal("Provider selection granted access to a missing provider ID")
			}
		})
	}
}

func TestAllProvidersStillValidatesIntegrationOriginsAndClients(t *testing.T) {
	integrationConfigEnvironment(t)
	t.Setenv("INTEGRATIONS_PROVIDER_ALLOWLIST", "all")
	t.Setenv("INTEGRATIONS_PUBLIC_BASE_URL", "http://api.tellbook.test")
	if _, err := LoadConfig("https://app.tellbook.test"); err == nil {
		t.Fatal("All-provider selection bypassed the HTTPS API origin requirement")
	}
	t.Setenv("INTEGRATIONS_PUBLIC_BASE_URL", "https://api.tellbook.test")
	if _, err := LoadConfig("http://app.tellbook.test"); err == nil {
		t.Fatal("All-provider selection bypassed the HTTPS client origin requirement")
	}
	t.Setenv("INTEGRATIONS_CLAUDE_CLIENT_METADATA_URL", "https://example.invalid/client.json")
	if _, err := LoadConfig("https://app.tellbook.test"); err == nil {
		t.Fatal("All-provider selection bypassed official OAuth client validation")
	}
}

func TestAllProvidersPreservesReadOnlyScopesAndRequiresAuthentication(t *testing.T) {
	integrationConfigEnvironment(t)
	t.Setenv("INTEGRATIONS_PROVIDER_ALLOWLIST", "all")
	cfg, err := LoadConfig("https://app.tellbook.test")
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{cfg: cfg}
	if !reflect.DeepEqual(s.availableScopes(), []string{"catalog.read"}) {
		t.Fatal("All-provider selection enabled scopes while writes were disabled")
	}
	for _, test := range []struct {
		name    string
		method  string
		handler http.HandlerFunc
	}{
		{"consent", http.MethodGet, s.consent},
		{"approve", http.MethodPost, s.approve},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			test.handler(response, httptest.NewRequest(test.method, "/v1/app/integrations/authorization-requests/test", nil))
			if response.Code != http.StatusForbidden {
				t.Fatalf("Unauthenticated request status = %d, want 403", response.Code)
			}
		})
	}
}

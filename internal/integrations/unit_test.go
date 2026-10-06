package integrations

import (
	"booking/go-server/internal/appdata"
	"errors"
	"reflect"
	"testing"
)

func TestAmountsLoadCurrencyOnce(t *testing.T) {
	calls := 0
	load := memoizedExponent(func() (uint8, error) { calls++; return 2, nil })
	body := map[string]any{
		"pricing":            map[string]any{"price_amount": "25.50", "compare_price_amount": "30", "deposit_amount": "5"},
		"fulfillment":        map[string]any{"travel_fee": "1.25"},
		"short_notice_rules": []any{map[string]any{"surcharge_amount": "2.50"}},
	}
	if err := convertAmounts(body, reflect.TypeOf(appdata.CreateManagedServiceInput{}), load); err != nil || calls != 1 {
		t.Fatalf("conversion: %v; currency reads: %d", err, calls)
	}
	if err := convertAmounts(map[string]any{"description": "Only text"}, reflect.TypeOf(appdata.CreateManagedServiceInput{}), memoizedExponent(func() (uint8, error) { t.Fatal("text edit loaded currency"); return 0, nil })); err != nil {
		t.Fatal(err)
	}
	want := errors.New("market unavailable")
	failed := memoizedExponent(func() (uint8, error) { calls++; return 0, want })
	for range 2 {
		if _, err := failed(); !errors.Is(err, want) {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatalf("failed lookup repeated: %d", calls)
	}
}

func TestToolSchemasAndDecimalAmounts(t *testing.T) {
	s := New(nil, nil, nil, Config{})
	if len(s.mcpHandlers) != 2 || len(catalogTools) != 15 {
		t.Fatal("missing platform or tools")
	}
	for _, tc := range []struct {
		decimal  string
		exponent uint8
		want     string
		bad      bool
	}{
		{"2500.50", 2, "250050", false}, {"1500", 0, "1500", false}, {"0.01", 2, "1", false}, {"1500.5", 0, "", true}, {"1.001", 2, "", true}, {"-1", 2, "", true}, {"9223372036854775808", 0, "", true},
	} {
		t.Run(tc.decimal, func(t *testing.T) {
			body := map[string]any{"pricing": map[string]any{"price_amount": tc.decimal}}
			e := convertAmounts(body, reflect.TypeOf(appdata.CreateManagedServiceInput{}), func() (uint8, error) { return tc.exponent, nil })
			if (e != nil) != tc.bad {
				t.Fatalf("error %v", e)
			}
			if !tc.bad && body["pricing"].(map[string]any)["price_amount_minor"] != tc.want {
				t.Fatalf("amount %v", body)
			}
		})
	}
}
func TestOAuthClientAndScopeRules(t *testing.T) {
	for _, v := range []string{"https://chatgpt.com.evil.test/oauth/client.json", "https://chatgpt.com:8443/oauth/client.json", "http://chatgpt.com/oauth/client.json", "https://user@chatgpt.com/oauth/client.json", "https://chatgpt.com/oauth/client.json?q=1"} {
		if officialURL("chatgpt", v) {
			t.Fatalf("accepted %s", v)
		}
	}
	if !officialURL("chatgpt", "https://chatgpt.com/oauth/client.json") {
		t.Fatal("official client rejected")
	}
	v := randomToken()
	if !validVerifier(v) || challenge(v) == v {
		t.Fatal("PKCE invalid")
	}
	for _, v := range []string{"short", randomToken() + " ", randomToken() + "💥"} {
		if validVerifier(v) {
			t.Fatal("invalid PKCE accepted")
		}
	}
	selected, e := parseScopes("catalog.write catalog.write offline_access")
	if e != nil || len(selected) != 2 || selected[0] != "catalog.read" {
		t.Fatal(selected, e)
	}
	if _, e = parseScopes("business.write"); e == nil {
		t.Fatal("unknown scope accepted")
	}
}

func TestCIMDAuthenticationIntersection(t *testing.T) {
	for _, tc := range []struct {
		d       clientDocument
		allowed bool
	}{
		{clientDocument{AuthMethod: "none"}, true},
		{clientDocument{AuthMethod: "private_key_jwt", AuthMethods: []string{"none", "private_key_jwt"}}, true},
		{clientDocument{AuthMethod: "private_key_jwt"}, false},
		{clientDocument{AuthMethod: "none", AuthMethods: []string{"private_key_jwt"}}, false},
		{clientDocument{}, false},
	} {
		if got := tc.d.supportsPublicPKCE(); got != tc.allowed {
			t.Fatalf("auth intersection: %v", tc.d)
		}
	}
}

func TestMCPPublicTunnelHostGuard(t *testing.T) {
	for _, tc := range []struct {
		host string
		ok   bool
	}{
		{"tellbook-beta-api.iycodes.com", true}, {"localhost:18200", true}, {"127.0.0.1:18200", true}, {"[::1]:18200", true},
		{"tellbook-beta-api.iycodes.com.evil.test", false}, {"tellbook-beta-api.iycodes.com:443", false}, {"localhost.evil.test", false}, {"evil.test", false}, {"", false},
	} {
		if got := allowedMCPHost("https://tellbook-beta-api.iycodes.com", tc.host); got != tc.ok {
			t.Fatalf("host %q: %v", tc.host, got)
		}
	}
}

package appdata

import (
	"testing"

	"github.com/google/uuid"
)

func TestInboxAIAutomationProviderAccess(t *testing.T) {
	first := uuid.MustParse("10000000-0000-4000-8000-000000000001")
	second := uuid.MustParse("10000000-0000-4000-8000-000000000002")
	for _, test := range []struct {
		name       string
		enabled    bool
		selection  []string
		allowFirst bool
		allowOther bool
	}{
		{"disabled all", false, []string{"all"}, false, false},
		{"all providers", true, []string{"all"}, true, true},
		{"normalized all", true, []string{"  ALL  "}, true, true},
		{"selected provider", true, []string{first.String()}, true, false},
		{"empty list", true, nil, false, false},
		{"invalid entry", true, []string{"invalid"}, false, false},
		{"mixed list cannot grant all", true, []string{"all", first.String()}, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := NewRepository(nil)
			repo.ConfigureInboxAIAutomation(test.enabled, test.selection)
			if got := repo.inboxAIAutomationAvailable(first); got != test.allowFirst {
				t.Fatalf("First provider access = %v, want %v", got, test.allowFirst)
			}
			if got := repo.inboxAIAutomationAvailable(second); got != test.allowOther {
				t.Fatalf("Other provider access = %v, want %v", got, test.allowOther)
			}
			if repo.inboxAIAutomationAvailable(uuid.Nil) {
				t.Fatal("Automation granted access to a missing provider ID")
			}
		})
	}
}

func TestInboxAIAutomationReconfigurationClearsAllProviderAccess(t *testing.T) {
	first := uuid.MustParse("10000000-0000-4000-8000-000000000001")
	second := uuid.MustParse("10000000-0000-4000-8000-000000000002")
	repo := NewRepository(nil)
	repo.ConfigureInboxAIAutomation(true, []string{"all"})
	if !repo.inboxAIAutomationAvailable(second) {
		t.Fatal("All-provider configuration did not grant access")
	}
	repo.ConfigureInboxAIAutomation(true, []string{first.String()})
	if !repo.inboxAIAutomationAvailable(first) || repo.inboxAIAutomationAvailable(second) {
		t.Fatal("Changing to a UUID list retained all-provider access")
	}
	repo.ConfigureInboxAIAutomation(true, []string{"all"})
	repo.ConfigureInboxAIAutomation(false, []string{"all"})
	if repo.inboxAIAutomationAvailable(first) || repo.inboxAIAutomationAvailable(second) {
		t.Fatal("Disabling automation retained provider access")
	}
	var missing *Repository
	if missing.inboxAIAutomationAvailable(first) {
		t.Fatal("Missing repository granted automation access")
	}
}

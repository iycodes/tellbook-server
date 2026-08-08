package domain

import "testing"

func TestAgreementVariableRegistryReturnsDefensiveCopies(t *testing.T) {
	registry := AgreementVariableRegistry()
	if len(registry) == 0 {
		t.Fatal("registry is empty")
	}
	registry[0].Label = "changed"
	registry[0].Contexts[0] = "changed"

	fresh := AgreementVariableRegistry()
	if fresh[0].Label == "changed" || fresh[0].Contexts[0] == "changed" {
		t.Fatal("registry exposed mutable package state")
	}
}

func TestAgreementVariableRegistryHasUniqueValidKeys(t *testing.T) {
	registry := AgreementVariableRegistry()
	keys := AgreementVariableKeySet()
	if len(keys) != len(registry) {
		t.Fatalf("key count %d does not match registry count %d", len(keys), len(registry))
	}
	for _, definition := range registry {
		if definition.Key == "" || definition.Label == "" || definition.Description == "" || definition.PreviewExample == "" {
			t.Fatalf("incomplete definition: %#v", definition)
		}
		if definition.EmptyBehavior != VariableEmptyError {
			t.Fatalf("unexpected empty behavior for %s: %q", definition.Key, definition.EmptyBehavior)
		}
		if len(definition.Contexts) == 0 {
			t.Fatalf("missing resolution contexts for %s", definition.Key)
		}
	}
}

func TestAgreementVariableReturnsDefinition(t *testing.T) {
	definition, ok := AgreementVariable("CUSTOMER_NAME")
	if !ok || definition.ValueType != VariableValueText {
		t.Fatalf("customer name definition = %#v, %v", definition, ok)
	}
	if _, ok := AgreementVariable("UNSUPPORTED"); ok {
		t.Fatal("unsupported key was returned")
	}
}

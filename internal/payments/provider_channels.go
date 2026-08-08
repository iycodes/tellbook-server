package payments

import "strings"

func CanonicalProviderChannel(value string) string {
	normalized := strings.NewReplacer("-", "_", " ", "_", "/", "_").Replace(
		strings.ToLower(strings.TrimSpace(value)),
	)
	switch normalized {
	case "card", "checkout_card":
		return "card"
	case "bank_transfer", "transfer", "virtual_account", "virtualaccount", "dynamic_virtual_account", "nip":
		return "bank_transfer"
	default:
		return ""
	}
}

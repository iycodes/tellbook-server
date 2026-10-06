package main

import (
	"booking/go-server/internal/config"
	"booking/go-server/internal/payments/capabilities"
)

// Scope the deployment's enabled capabilities to its selected payment environment.
// The registry still rejects requests for the other environment.
func paymentCapabilityReadiness(cfg config.Config, configured bool, provider, capability string) capabilities.CapabilityReadiness {
	enabled := cfg.PaymentCapabilityEnabled(provider, capability)
	return capabilities.CapabilityReadiness{
		Configured:        configured,
		SandboxVerified:   enabled && cfg.PaymentsEnvironment == string(capabilities.EnvironmentTest),
		ProductionEnabled: enabled && cfg.PaymentsEnvironment == string(capabilities.EnvironmentLive),
	}
}

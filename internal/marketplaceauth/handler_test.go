package marketplaceauth

import (
	"testing"

	"booking/go-server/internal/config"
)

func TestNotificationCapabilitiesRespectDeliveryAndTemplateGates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  config.Config
		want NotificationDeliveryCapabilities
	}{
		{
			name: "disabled workers",
			cfg:  config.Config{},
			want: NotificationDeliveryCapabilities{},
		},
		{
			name: "email worker enabled",
			cfg:  config.Config{NotificationEmailEnabled: true},
			want: NotificationDeliveryCapabilities{EmailReminderAvailable: true},
		},
		{
			name: "enabled customer WhatsApp template can be advertised",
			cfg: config.Config{
				NotificationWhatsAppEnabled: true,
				WhatsAppEnabledTemplateKeys: []string{"user_reminder"},
			},
			want: NotificationDeliveryCapabilities{WhatsAppReminderAvailable: true},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			handler := NewHandler(nil, nil, test.cfg)
			if got := handler.NotificationCapabilities(); got != test.want {
				t.Fatalf("capabilities = %#v, want %#v", got, test.want)
			}
		})
	}
}

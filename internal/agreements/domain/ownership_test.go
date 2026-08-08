package domain

import (
	"testing"

	"github.com/google/uuid"
)

func TestTemplateOwnerEnforcesTenantShape(t *testing.T) {
	clientID := uuid.New()
	tests := []struct {
		name    string
		owner   TemplateOwner
		wantErr bool
	}{
		{name: "system", owner: SystemTemplateOwner()},
		{name: "client", owner: ClientTemplateOwner(clientID)},
		{name: "system with client", owner: TemplateOwner{Type: OwnerTypeSystem, ClientID: &clientID}, wantErr: true},
		{name: "client without client", owner: TemplateOwner{Type: OwnerTypeClient}, wantErr: true},
		{name: "unknown", owner: TemplateOwner{Type: "shared"}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.owner.Validate(); (err != nil) != test.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
	if !ClientTemplateOwner(clientID).IsOwnedBy(clientID) {
		t.Fatal("client could not access its template")
	}
	if ClientTemplateOwner(clientID).IsOwnedBy(uuid.New()) {
		t.Fatal("different client accessed template")
	}
}

package domain

import (
	"fmt"

	"github.com/google/uuid"
)

type TemplateOwner struct {
	Type     OwnerType
	ClientID *uuid.UUID
}

func SystemTemplateOwner() TemplateOwner {
	return TemplateOwner{Type: OwnerTypeSystem}
}

func ClientTemplateOwner(clientID uuid.UUID) TemplateOwner {
	return TemplateOwner{Type: OwnerTypeClient, ClientID: &clientID}
}

func (o TemplateOwner) Validate() error {
	if _, err := ParseOwnerType(string(o.Type)); err != nil {
		return err
	}
	switch o.Type {
	case OwnerTypeSystem:
		if o.ClientID != nil {
			return fmt.Errorf("system template owner must not have a client ID")
		}
	case OwnerTypeClient:
		if o.ClientID == nil || *o.ClientID == uuid.Nil {
			return fmt.Errorf("client template owner requires a client ID")
		}
	}
	return nil
}

func (o TemplateOwner) IsOwnedBy(clientID uuid.UUID) bool {
	return o.Validate() == nil && o.Type == OwnerTypeClient && clientID != uuid.Nil && *o.ClientID == clientID
}

func (o TemplateOwner) IsSystemLibrary() bool {
	return o.Validate() == nil && o.Type == OwnerTypeSystem
}

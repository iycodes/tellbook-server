package appdata

import (
	"net/http"

	"booking/go-server/internal/agreements/domain"
	aiapi "booking/shared/ai_api"
)

type agreementDocumentValidationLimits struct {
	MaxBlocks            int `json:"max_blocks"`
	MaxListItemsPerBlock int `json:"max_list_items_per_block"`
	MaxInlineNodes       int `json:"max_inline_nodes_per_block"`
	MaxVisibleCharacters int `json:"max_visible_characters"`
}

type agreementTemplateVariableRegistryResponse struct {
	SchemaVersion int                               `json:"schema_version"`
	Limits        agreementDocumentValidationLimits `json:"limits"`
	Items         []domain.VariableDefinition       `json:"items"`
}

func (h *Handler) listAgreementTemplateVariables(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, agreementTemplateVariableRegistryResponse{
		SchemaVersion: aiapi.AgreementDocumentSchemaVersion,
		Limits: agreementDocumentValidationLimits{
			MaxBlocks:            aiapi.AgreementDocumentMaxBlocks,
			MaxListItemsPerBlock: aiapi.AgreementDocumentMaxListItems,
			MaxInlineNodes:       aiapi.AgreementDocumentMaxInline,
			MaxVisibleCharacters: aiapi.AgreementDocumentMaxCharacters,
		},
		Items: domain.AgreementVariableRegistry(),
	})
}

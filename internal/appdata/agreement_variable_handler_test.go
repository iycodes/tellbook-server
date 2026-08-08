package appdata

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestListAgreementTemplateVariables(t *testing.T) {
	handler := &Handler{}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1/app/agreement-template-variables", nil)

	handler.listAgreementTemplateVariables(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	var response agreementTemplateVariableRegistryResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.SchemaVersion != 1 || response.Limits.MaxBlocks == 0 || len(response.Items) == 0 {
		t.Fatalf("incomplete registry response: %#v", response)
	}
	if strings.Contains(recorder.Body.String(), "legacy_aliases") {
		t.Fatal("registry response contains legacy aliases")
	}
}

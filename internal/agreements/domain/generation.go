package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	aiapi "booking/go-server/shared/ai_api"
)

type FieldsGenerationInput struct {
	BusinessCategory          string             `json:"business_category"`
	ServiceName               string             `json:"service_name"`
	CustomInstructions        string             `json:"custom_instructions"`
	AgreementStyle            string             `json:"agreement_style"`
	TypicalServiceLocation    string             `json:"typical_service_location"`
	Tone                      string             `json:"tone"`
	IncludeCancellationPolicy bool               `json:"include_cancellation_policy"`
	IncludeLatenessPolicy     bool               `json:"include_lateness_policy"`
	IncludePaymentTerms       bool               `json:"include_payment_terms"`
	Context                   []aiapi.NamedValue `json:"context"`
}

type UploadGenerationInput struct {
	SourcePDFR2Key     string             `json:"source_pdf_r2_key"`
	SourcePDFFileName  string             `json:"source_pdf_file_name"`
	SourceTitle        string             `json:"source_title"`
	BusinessCategory   string             `json:"business_category"`
	ServiceName        string             `json:"service_name"`
	CustomInstructions string             `json:"custom_instructions"`
	Context            []aiapi.NamedValue `json:"context"`
}

func EncodeGenerationInput(kind GenerationInputKind, input any) (json.RawMessage, error) {
	if err := validateGenerationInput(kind, input); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("encode agreement generation input: %w", err)
	}
	return payload, nil
}

func DecodeFieldsGenerationInput(payload json.RawMessage) (FieldsGenerationInput, error) {
	var input FieldsGenerationInput
	if err := decodeGenerationInput(payload, &input); err != nil {
		return FieldsGenerationInput{}, err
	}
	if err := validateGenerationInput(GenerationInputFields, input); err != nil {
		return FieldsGenerationInput{}, err
	}
	return input, nil
}

func DecodeUploadGenerationInput(payload json.RawMessage) (UploadGenerationInput, error) {
	var input UploadGenerationInput
	if err := decodeGenerationInput(payload, &input); err != nil {
		return UploadGenerationInput{}, err
	}
	if err := validateGenerationInput(GenerationInputUpload, input); err != nil {
		return UploadGenerationInput{}, err
	}
	return input, nil
}

func validateGenerationInput(kind GenerationInputKind, input any) error {
	if _, err := ParseGenerationInputKind(string(kind)); err != nil {
		return err
	}
	switch kind {
	case GenerationInputFields:
		value, ok := input.(FieldsGenerationInput)
		if !ok {
			return fmt.Errorf("fields generation input has type %T", input)
		}
		if strings.TrimSpace(value.BusinessCategory) == "" || strings.TrimSpace(value.ServiceName) == "" {
			return fmt.Errorf("business category and service name are required")
		}
	case GenerationInputUpload:
		value, ok := input.(UploadGenerationInput)
		if !ok {
			return fmt.Errorf("upload generation input has type %T", input)
		}
		if strings.TrimSpace(value.SourcePDFR2Key) == "" || strings.TrimSpace(value.SourcePDFFileName) == "" {
			return fmt.Errorf("owned source PDF key and file name are required")
		}
	default:
		return fmt.Errorf("unsupported generation input kind %q", kind)
	}
	return nil
}

func decodeGenerationInput(payload json.RawMessage, destination any) error {
	if len(payload) == 0 {
		return fmt.Errorf("agreement generation input is empty")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("decode agreement generation input: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("agreement generation input contains trailing data")
		}
		return fmt.Errorf("decode trailing agreement generation input: %w", err)
	}
	return nil
}

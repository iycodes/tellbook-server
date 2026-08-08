package worker

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"booking/go-server/internal/agreements/domain"
	aiapi "booking/shared/ai_api"
)

type UploadPreparation struct {
	RedactedDocumentText string
	ProhibitedLiterals   []string
}

type UploadPreparer interface {
	PrepareAgreementUpload(context.Context, domain.TemplateGenerationJob, domain.UploadGenerationInput) (UploadPreparation, error)
}

type StoredGenerationRequestBuilder struct {
	uploadPreparer UploadPreparer
}

func NewStoredGenerationRequestBuilder(uploadPreparer UploadPreparer) (*StoredGenerationRequestBuilder, error) {
	return &StoredGenerationRequestBuilder{uploadPreparer: uploadPreparer}, nil
}

func (b *StoredGenerationRequestBuilder) Build(
	ctx context.Context,
	job domain.TemplateGenerationJob,
) (PreparedGenerationRequest, error) {
	if b == nil {
		return PreparedGenerationRequest{}, permanentGenerationError("generation_builder_unavailable", "agreement generation request builder is not configured")
	}
	request := aiapi.GenerateAgreementDocumentRequest{
		SchemaVersion:      aiapi.AgreementDocumentSchemaVersion,
		ConfirmationMethod: job.ConfirmationMethod.AIAPIValue(),
		SupportedVariables: supportedAgreementVariables(),
	}
	var guard OutputGuard = StandardOutputGuard{}
	switch job.InputKind {
	case domain.GenerationInputFields:
		input, err := domain.DecodeFieldsGenerationInput(job.InputJSON)
		if err != nil {
			return PreparedGenerationRequest{}, permanentGenerationError("invalid_generation_input", err.Error())
		}
		request.Source = aiapi.AgreementGenerationFromFields
		request.BusinessCategory = strings.TrimSpace(input.BusinessCategory)
		request.ServiceName = strings.TrimSpace(input.ServiceName)
		request.CustomInstructions = strings.TrimSpace(input.CustomInstructions)
		request.AgreementStyle = strings.TrimSpace(input.AgreementStyle)
		request.TypicalServiceLocation = strings.TrimSpace(input.TypicalServiceLocation)
		request.Tone = strings.TrimSpace(input.Tone)
		request.IncludeCancellationPolicy = input.IncludeCancellationPolicy
		request.IncludeLatenessPolicy = input.IncludeLatenessPolicy
		request.IncludePaymentTerms = input.IncludePaymentTerms
	case domain.GenerationInputUpload:
		if b.uploadPreparer == nil {
			return PreparedGenerationRequest{}, permanentGenerationError("upload_storage_unavailable", "private agreement storage is not configured")
		}
		input, err := domain.DecodeUploadGenerationInput(job.InputJSON)
		if err != nil {
			return PreparedGenerationRequest{}, permanentGenerationError("invalid_generation_input", err.Error())
		}
		prepared, err := b.uploadPreparer.PrepareAgreementUpload(ctx, job, input)
		if err != nil {
			return PreparedGenerationRequest{}, err
		}
		if strings.TrimSpace(prepared.RedactedDocumentText) == "" {
			return PreparedGenerationRequest{}, permanentGenerationError("empty_pdf_text", "the uploaded PDF contains no usable text")
		}
		request.Source = aiapi.AgreementGenerationFromDocument
		request.SourceTitle = strings.TrimSpace(input.SourceTitle)
		request.SourceFileName = strings.TrimSpace(input.SourcePDFFileName)
		request.RedactedDocumentText = prepared.RedactedDocumentText
		request.BusinessCategory = strings.TrimSpace(input.BusinessCategory)
		request.ServiceName = strings.TrimSpace(input.ServiceName)
		request.CustomInstructions = strings.TrimSpace(input.CustomInstructions)
		guard = NewUploadOutputGuard(prepared.RedactedDocumentText, prepared.ProhibitedLiterals)
	default:
		return PreparedGenerationRequest{}, permanentGenerationError("invalid_generation_input", fmt.Sprintf("unsupported input kind %q", job.InputKind))
	}
	return PreparedGenerationRequest{Request: request, Guard: guard}, nil
}

func supportedAgreementVariables() []aiapi.SupportedAgreementVariable {
	registry := domain.AgreementVariableRegistry()
	result := make([]aiapi.SupportedAgreementVariable, len(registry))
	for index, variable := range registry {
		result[index] = aiapi.SupportedAgreementVariable{
			Key:          variable.Key,
			Label:        variable.Label,
			Description:  variable.Description,
			ExampleValue: variable.PreviewExample,
		}
	}
	return result
}

var (
	emailPattern   = regexp.MustCompile(`(?i)\b[a-z0-9.!#$%&'*+/=?^_` + "`" + `{|}~-]+@[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+\b`)
	phonePattern   = regexp.MustCompile(`(?:\+?\d[\d ()-]{8,}\d)`)
	accountPattern = regexp.MustCompile(`\b\d{10,16}\b`)
)

type StandardOutputGuard struct{}

func (StandardOutputGuard) Validate(response aiapi.GenerateAgreementDocumentResponse) error {
	content := generatedResponseVisibleText(response)
	if emailPattern.MatchString(content) || phonePattern.MatchString(content) || accountPattern.MatchString(content) {
		return errors.New("generated agreement contains contact or account information")
	}
	return nil
}

type UploadOutputGuard struct {
	prohibitedLiterals []string
	sourcePhrases      map[string]struct{}
}

func NewUploadOutputGuard(redactedSource string, prohibitedLiterals []string) UploadOutputGuard {
	normalizedLiterals := make([]string, 0, len(prohibitedLiterals))
	for _, value := range prohibitedLiterals {
		value = strings.ToLower(strings.TrimSpace(value))
		if len([]rune(value)) >= 4 {
			normalizedLiterals = append(normalizedLiterals, value)
		}
	}
	return UploadOutputGuard{
		prohibitedLiterals: normalizedLiterals,
		sourcePhrases:      phraseSet(normalizedWords(redactedSource), 13),
	}
}

func (g UploadOutputGuard) Validate(response aiapi.GenerateAgreementDocumentResponse) error {
	if err := (StandardOutputGuard{}).Validate(response); err != nil {
		return err
	}
	content := strings.ToLower(generatedResponseVisibleText(response))
	for _, literal := range g.prohibitedLiterals {
		if strings.Contains(content, literal) {
			return errors.New("generated agreement contains identifying source content")
		}
	}
	words := normalizedWords(content)
	for phrase := range phraseSet(words, 13) {
		if _, exists := g.sourcePhrases[phrase]; exists {
			return errors.New("generated agreement copies too much source wording")
		}
	}
	return nil
}

func generatedResponseVisibleText(response aiapi.GenerateAgreementDocumentResponse) string {
	var output strings.Builder
	output.WriteString(response.SuggestedTitle)
	output.WriteByte('\n')
	output.WriteString(response.SuggestedDescription)
	if response.DocumentSchema == nil {
		return output.String()
	}
	for _, block := range response.DocumentSchema.Blocks {
		for _, node := range block.Content {
			if node.Type == aiapi.AgreementInlineText {
				output.WriteByte('\n')
				output.WriteString(node.Text)
			}
		}
		for _, item := range block.Items {
			for _, node := range item {
				if node.Type == aiapi.AgreementInlineText {
					output.WriteByte('\n')
					output.WriteString(node.Text)
				}
			}
		}
	}
	return output.String()
}

func normalizedWords(value string) []string {
	return strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
}

func phraseSet(words []string, size int) map[string]struct{} {
	result := make(map[string]struct{})
	if size <= 0 || len(words) < size {
		return result
	}
	for index := 0; index+size <= len(words); index++ {
		result[strings.Join(words[index:index+size], " ")] = struct{}{}
	}
	return result
}

package domain

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	aiapi "booking/shared/ai_api"

	"github.com/google/uuid"
)

type TemplateFamily struct {
	ID                        uuid.UUID
	Owner                     TemplateOwner
	Title                     string
	Description               string
	Category                  string
	Tags                      []string
	ConfirmationMethod        ConfirmationMethod
	Status                    TemplateFamilyStatus
	CurrentPublishedVersionID *uuid.UUID
	SourceFamilyID            *uuid.UUID
	CreatedByClientID         *uuid.UUID
	CreatedAt                 time.Time
	UpdatedAt                 time.Time
	ArchivedAt                *time.Time
}

type TemplateVersion struct {
	ID                 uuid.UUID
	FamilyID           uuid.UUID
	VersionNumber      int
	State              TemplateVersionState
	Document           *aiapi.DocumentSchema
	UsedVariableKeys   []string
	SchemaVersion      int
	RendererVersion    int
	SourceKind         TemplateSourceKind
	SourcePDFR2Key     string
	SourcePDFFileName  string
	TemplateSchemaHash string
	ReviewWarnings     json.RawMessage
	Revision           int64
	PublishedAt        *time.Time
	CreatedByClientID  *uuid.UUID
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type TemplateGenerationJob struct {
	ID                 uuid.UUID
	ClientID           uuid.UUID
	FamilyID           uuid.UUID
	VersionID          uuid.UUID
	ConfirmationMethod ConfirmationMethod
	InputKind          GenerationInputKind
	InputJSON          json.RawMessage
	Status             JobStatus
	AttemptCount       int
	MaxAttempts        int
	RunAt              time.Time
	LeaseOwner         string
	LeaseExpiresAt     *time.Time
	ErrorCode          string
	ErrorMessage       string
	CreatedAt          time.Time
	StartedAt          *time.Time
	CompletedAt        *time.Time
	UpdatedAt          time.Time
}

type AgreementInstance struct {
	ID                       uuid.UUID
	ClientID                 uuid.UUID
	CustomerID               *uuid.UUID
	BookingID                *uuid.UUID
	TemplateFamilyID         *uuid.UUID
	TemplateVersionID        *uuid.UUID
	TitleSnapshot            string
	BookingSummarySnapshot   json.RawMessage
	ResolvedDocumentSnapshot aiapi.DocumentSchema
	SchemaVersionSnapshot    int
	RendererVersionSnapshot  int
	RenderedHTMLSnapshot     string
	ResolvedTermsHash        string
	ConfirmationMethod       ConfirmationMethod
	Timing                   AgreementTiming
	Status                   AgreementStatus
	PublicTokenHash          []byte
	PublicTokenCiphertext    []byte
	PublicTokenNonce         []byte
	PublicTokenKeyVersion    string
	SentToEmail              string
	PersonalMessageSnapshot  string
	DeliveryRevision         int
	ExpiresAt                *time.Time
	CompletedAt              *time.Time
	PDFStatus                PDFStatus
	PDFR2Key                 string
	PDFSHA256                string
	PDFErrorCode             string
	CreatedAt                time.Time
	UpdatedAt                time.Time
}

type AgreementAcceptance struct {
	AgreementID       uuid.UUID
	Method            ConfirmationMethod
	SignerName        string
	SignaturePNG      []byte
	SignatureSHA256   string
	AcceptedAt        time.Time
	ResolvedTermsHash string
	CreatedAt         time.Time
}

type StandaloneSignature struct {
	BookingID       uuid.UUID
	ClientID        uuid.UUID
	CustomerID      uuid.UUID
	SignerName      string
	SignaturePNG    []byte
	SignatureSHA256 string
	AcceptedAt      time.Time
	CreatedAt       time.Time
}

type AgreementEvent struct {
	ID          uuid.UUID
	AgreementID uuid.UUID
	EventType   AgreementEventType
	ActorType   AgreementActorType
	DedupeKey   string
	Metadata    json.RawMessage
	OccurredAt  time.Time
}

type AgreementJob struct {
	ID             uuid.UUID
	AgreementID    uuid.UUID
	Kind           AgreementJobKind
	DedupeKey      string
	Status         JobStatus
	AttemptCount   int
	MaxAttempts    int
	RunAt          time.Time
	LeaseOwner     string
	LeaseExpiresAt *time.Time
	ErrorCode      string
	ErrorMessage   string
	CreatedAt      time.Time
	CompletedAt    *time.Time
	UpdatedAt      time.Time
}

func (f TemplateFamily) Validate() error {
	if f.ID == uuid.Nil {
		return fmt.Errorf("template family ID is required")
	}
	if err := f.Owner.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(f.Title) == "" || strings.TrimSpace(f.Category) == "" {
		return fmt.Errorf("template family title and category are required")
	}
	if _, err := ParseConfirmationMethod(string(f.ConfirmationMethod)); err != nil {
		return err
	}
	if _, err := ParseTemplateFamilyStatus(string(f.Status)); err != nil {
		return err
	}
	if f.Status == TemplateFamilyPublished && f.CurrentPublishedVersionID == nil {
		return fmt.Errorf("published template family requires a current published version")
	}
	return nil
}

func (v TemplateVersion) Validate(method ConfirmationMethod) error {
	if v.ID == uuid.Nil || v.FamilyID == uuid.Nil {
		return fmt.Errorf("template version and family IDs are required")
	}
	if v.VersionNumber <= 0 || v.Revision <= 0 {
		return fmt.Errorf("template version number and revision must be positive")
	}
	if _, err := ParseTemplateVersionState(string(v.State)); err != nil {
		return err
	}
	if _, err := ParseTemplateSourceKind(string(v.SourceKind)); err != nil {
		return err
	}
	if v.Document == nil {
		if v.State != TemplateVersionDraft || v.TemplateSchemaHash != "" || len(v.UsedVariableKeys) != 0 {
			return fmt.Errorf("only an incomplete draft may omit its document")
		}
		return nil
	}
	if err := ValidateDocument(*v.Document, method, AgreementVariableKeySet()); err != nil {
		return fmt.Errorf("validate template version document: %w", err)
	}
	if v.SchemaVersion != aiapi.AgreementDocumentSchemaVersion || v.RendererVersion <= 0 {
		return fmt.Errorf("template version schema and renderer versions are unsupported")
	}
	wantHash, err := TemplateSchemaHash(*v.Document, method)
	if err != nil {
		return err
	}
	if v.TemplateSchemaHash != wantHash {
		return fmt.Errorf("template schema hash does not match document")
	}
	if !equalStringSlices(v.UsedVariableKeys, v.Document.VariableKeys()) {
		return fmt.Errorf("used variable keys do not match document")
	}
	if v.State == TemplateVersionPublished && v.PublishedAt == nil {
		return fmt.Errorf("published template version requires published_at")
	}
	return nil
}

func equalStringSlices(first, second []string) bool {
	if len(first) != len(second) {
		return false
	}
	for index := range first {
		if first[index] != second[index] {
			return false
		}
	}
	return true
}

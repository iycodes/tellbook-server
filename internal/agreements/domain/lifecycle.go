package domain

import "fmt"

type OwnerType string

const (
	OwnerTypeSystem OwnerType = "system"
	OwnerTypeClient OwnerType = "client"
)

type TemplateFamilyStatus string

const (
	TemplateFamilyDraft     TemplateFamilyStatus = "draft"
	TemplateFamilyPublished TemplateFamilyStatus = "published"
	TemplateFamilyArchived  TemplateFamilyStatus = "archived"
)

type TemplateVersionState string

const (
	TemplateVersionDraft     TemplateVersionState = "draft"
	TemplateVersionPublished TemplateVersionState = "published"
	TemplateVersionRetired   TemplateVersionState = "retired"
)

type TemplateSourceKind string

const (
	TemplateSourceAI          TemplateSourceKind = "ai"
	TemplateSourceUpload      TemplateSourceKind = "upload"
	TemplateSourceLibraryCopy TemplateSourceKind = "library_copy"
	TemplateSourceSystemSeed  TemplateSourceKind = "system_seed"
)

type AgreementTiming string

const (
	AgreementTimingBeforePayment AgreementTiming = "before_payment"
	AgreementTimingAfterPayment  AgreementTiming = "after_payment"
	AgreementTimingManual        AgreementTiming = "manual"
)

type AgreementStatus string

const (
	AgreementStatusDraft            AgreementStatus = "draft"
	AgreementStatusAwaitingCustomer AgreementStatus = "awaiting_customer"
	AgreementStatusCompleted        AgreementStatus = "completed"
	AgreementStatusExpired          AgreementStatus = "expired"
	AgreementStatusCancelled        AgreementStatus = "cancelled"
)

type PDFStatus string

const (
	PDFStatusNotRequested PDFStatus = "not_requested"
	PDFStatusQueued       PDFStatus = "queued"
	PDFStatusProcessing   PDFStatus = "processing"
	PDFStatusReady        PDFStatus = "ready"
	PDFStatusFailed       PDFStatus = "failed"
)

type JobStatus string

const (
	JobStatusQueued     JobStatus = "queued"
	JobStatusProcessing JobStatus = "processing"
	JobStatusCompleted  JobStatus = "completed"
	JobStatusFailed     JobStatus = "failed"
)

type GenerationInputKind string

const (
	GenerationInputFields GenerationInputKind = "fields"
	GenerationInputUpload GenerationInputKind = "upload"
)

type AgreementJobKind string

const (
	AgreementJobRenderCompletedPDF AgreementJobKind = "render_completed_pdf"
	AgreementJobSendAgreementEmail AgreementJobKind = "send_agreement_email"
	AgreementJobSendCompletedEmail AgreementJobKind = "send_completed_email"
)

type AgreementEventType string

const (
	AgreementEventCreated        AgreementEventType = "created"
	AgreementEventSent           AgreementEventType = "sent"
	AgreementEventDeliveryFailed AgreementEventType = "delivery_failed"
	AgreementEventViewed         AgreementEventType = "viewed"
	AgreementEventCompleted      AgreementEventType = "completed"
	AgreementEventPDFReady       AgreementEventType = "pdf_ready"
	AgreementEventPDFFailed      AgreementEventType = "pdf_failed"
	AgreementEventResent         AgreementEventType = "resent"
	AgreementEventExpired        AgreementEventType = "expired"
	AgreementEventCancelled      AgreementEventType = "cancelled"
)

type AgreementActorType string

const (
	AgreementActorSystem   AgreementActorType = "system"
	AgreementActorBusiness AgreementActorType = "business"
	AgreementActorCustomer AgreementActorType = "customer"
)

func ParseOwnerType(value string) (OwnerType, error) {
	return parseDomainValue("owner type", value, OwnerTypeSystem, OwnerTypeClient)
}

func ParseTemplateFamilyStatus(value string) (TemplateFamilyStatus, error) {
	return parseDomainValue("template family status", value, TemplateFamilyDraft, TemplateFamilyPublished, TemplateFamilyArchived)
}

func ParseTemplateVersionState(value string) (TemplateVersionState, error) {
	return parseDomainValue("template version state", value, TemplateVersionDraft, TemplateVersionPublished, TemplateVersionRetired)
}

func ParseTemplateSourceKind(value string) (TemplateSourceKind, error) {
	return parseDomainValue("template source kind", value, TemplateSourceAI, TemplateSourceUpload, TemplateSourceLibraryCopy, TemplateSourceSystemSeed)
}

func ParseAgreementTiming(value string) (AgreementTiming, error) {
	return parseDomainValue("agreement timing", value, AgreementTimingBeforePayment, AgreementTimingAfterPayment, AgreementTimingManual)
}

func ParseAgreementStatus(value string) (AgreementStatus, error) {
	return parseDomainValue("agreement status", value, AgreementStatusDraft, AgreementStatusAwaitingCustomer, AgreementStatusCompleted, AgreementStatusExpired, AgreementStatusCancelled)
}

func ParsePDFStatus(value string) (PDFStatus, error) {
	return parseDomainValue("PDF status", value, PDFStatusNotRequested, PDFStatusQueued, PDFStatusProcessing, PDFStatusReady, PDFStatusFailed)
}

func ParseJobStatus(value string) (JobStatus, error) {
	return parseDomainValue("job status", value, JobStatusQueued, JobStatusProcessing, JobStatusCompleted, JobStatusFailed)
}

func ParseGenerationInputKind(value string) (GenerationInputKind, error) {
	return parseDomainValue("generation input kind", value, GenerationInputFields, GenerationInputUpload)
}

func ParseAgreementJobKind(value string) (AgreementJobKind, error) {
	return parseDomainValue("agreement job kind", value, AgreementJobRenderCompletedPDF, AgreementJobSendAgreementEmail, AgreementJobSendCompletedEmail)
}

func ParseAgreementEventType(value string) (AgreementEventType, error) {
	return parseDomainValue(
		"agreement event type",
		value,
		AgreementEventCreated,
		AgreementEventSent,
		AgreementEventDeliveryFailed,
		AgreementEventViewed,
		AgreementEventCompleted,
		AgreementEventPDFReady,
		AgreementEventPDFFailed,
		AgreementEventResent,
		AgreementEventExpired,
		AgreementEventCancelled,
	)
}

func ParseAgreementActorType(value string) (AgreementActorType, error) {
	return parseDomainValue("agreement actor type", value, AgreementActorSystem, AgreementActorBusiness, AgreementActorCustomer)
}

func CanTransitionTemplateFamily(from, to TemplateFamilyStatus, hasPublishedVersion bool) bool {
	if from == to {
		return true
	}
	switch from {
	case TemplateFamilyDraft:
		return to == TemplateFamilyPublished || to == TemplateFamilyArchived
	case TemplateFamilyPublished:
		return to == TemplateFamilyArchived
	case TemplateFamilyArchived:
		if hasPublishedVersion {
			return to == TemplateFamilyPublished
		}
		return to == TemplateFamilyDraft
	default:
		return false
	}
}

func CanTransitionTemplateVersion(from, to TemplateVersionState) bool {
	if from == to {
		return true
	}
	return (from == TemplateVersionDraft && to == TemplateVersionPublished) ||
		(from == TemplateVersionPublished && to == TemplateVersionRetired)
}

func CanTransitionAgreement(from, to AgreementStatus) bool {
	if from == to {
		return true
	}
	switch from {
	case AgreementStatusDraft:
		return to == AgreementStatusAwaitingCustomer || to == AgreementStatusCancelled
	case AgreementStatusAwaitingCustomer:
		return to == AgreementStatusCompleted || to == AgreementStatusExpired || to == AgreementStatusCancelled
	default:
		return false
	}
}

func CanTransitionJob(from, to JobStatus) bool {
	if from == to {
		return true
	}
	switch from {
	case JobStatusQueued:
		return to == JobStatusProcessing
	case JobStatusProcessing:
		return to == JobStatusQueued || to == JobStatusCompleted || to == JobStatusFailed
	case JobStatusFailed:
		return to == JobStatusQueued
	default:
		return false
	}
}

func parseDomainValue[T ~string](label, value string, allowed ...T) (T, error) {
	candidate := T(value)
	for _, item := range allowed {
		if candidate == item {
			return candidate, nil
		}
	}
	var zero T
	return zero, fmt.Errorf("unsupported %s %q", label, value)
}

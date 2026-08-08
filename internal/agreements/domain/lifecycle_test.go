package domain

import "testing"

func TestDomainParsersRejectUnknownValues(t *testing.T) {
	tests := []struct {
		name  string
		parse func(string) error
	}{
		{name: "owner", parse: func(value string) error { _, err := ParseOwnerType(value); return err }},
		{name: "family", parse: func(value string) error { _, err := ParseTemplateFamilyStatus(value); return err }},
		{name: "version", parse: func(value string) error { _, err := ParseTemplateVersionState(value); return err }},
		{name: "source", parse: func(value string) error { _, err := ParseTemplateSourceKind(value); return err }},
		{name: "timing", parse: func(value string) error { _, err := ParseAgreementTiming(value); return err }},
		{name: "agreement", parse: func(value string) error { _, err := ParseAgreementStatus(value); return err }},
		{name: "pdf", parse: func(value string) error { _, err := ParsePDFStatus(value); return err }},
		{name: "job", parse: func(value string) error { _, err := ParseJobStatus(value); return err }},
		{name: "input", parse: func(value string) error { _, err := ParseGenerationInputKind(value); return err }},
		{name: "job kind", parse: func(value string) error { _, err := ParseAgreementJobKind(value); return err }},
		{name: "event", parse: func(value string) error { _, err := ParseAgreementEventType(value); return err }},
		{name: "actor", parse: func(value string) error { _, err := ParseAgreementActorType(value); return err }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.parse("unknown"); err == nil {
				t.Fatal("unknown value was accepted")
			}
		})
	}
}

func TestAgreementTransitions(t *testing.T) {
	allowed := [][2]AgreementStatus{
		{AgreementStatusDraft, AgreementStatusAwaitingCustomer},
		{AgreementStatusDraft, AgreementStatusCancelled},
		{AgreementStatusAwaitingCustomer, AgreementStatusCompleted},
		{AgreementStatusAwaitingCustomer, AgreementStatusExpired},
		{AgreementStatusAwaitingCustomer, AgreementStatusCancelled},
	}
	for _, transition := range allowed {
		if !CanTransitionAgreement(transition[0], transition[1]) {
			t.Fatalf("expected transition %s -> %s", transition[0], transition[1])
		}
	}
	if CanTransitionAgreement(AgreementStatusCompleted, AgreementStatusAwaitingCustomer) {
		t.Fatal("completed agreement transitioned backwards")
	}
}

func TestArchivedFamilyRestoreUsesPublicationHistory(t *testing.T) {
	if !CanTransitionTemplateFamily(TemplateFamilyArchived, TemplateFamilyPublished, true) {
		t.Fatal("published family could not be restored")
	}
	if !CanTransitionTemplateFamily(TemplateFamilyArchived, TemplateFamilyDraft, false) {
		t.Fatal("unpublished family could not be restored to draft")
	}
	if CanTransitionTemplateFamily(TemplateFamilyArchived, TemplateFamilyDraft, true) {
		t.Fatal("published family restored to draft")
	}
}

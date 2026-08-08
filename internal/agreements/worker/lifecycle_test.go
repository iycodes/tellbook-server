package worker

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestAgreementJobBackoffIsBounded(t *testing.T) {
	if got := agreementJobBackoff(1); got != 15*time.Second {
		t.Fatalf("first retry delay = %s", got)
	}
	if got := agreementJobBackoff(20); got != 15*time.Minute {
		t.Fatalf("maximum retry delay = %s", got)
	}
}

func TestAgreementEmailMessageIDIsStablePerJob(t *testing.T) {
	agreementID := uuid.New()
	first := agreementEmailMessageID(agreementID, "initial-email:agreement:0")
	if first != agreementEmailMessageID(agreementID, "initial-email:agreement:0") {
		t.Fatal("message ID changed for the same agreement job")
	}
	if first == agreementEmailMessageID(agreementID, "resend-email:agreement:1") {
		t.Fatal("different delivery jobs received the same message ID")
	}
}

package aierror

import (
	"errors"
	"strings"
	"testing"
)

func TestErrorClassificationAndSafeMessage(t *testing.T) {
	privateCause := errors.New("private customer address")
	err := Transient("call external model", KindUnavailable, privateCause)
	if !IsRetryable(err) || IsRepairable(err) || IsTerminal(err) {
		t.Fatalf("unexpected transient classification: %v", err)
	}
	if strings.Contains(err.Error(), privateCause.Error()) || !errors.Is(err, privateCause) {
		t.Fatalf("error either leaked or lost its cause: %v", err)
	}

	err = InvalidOutput("decode external model", privateCause)
	if IsRetryable(err) || !IsRepairable(err) || !IsTerminal(err) {
		t.Fatalf("unexpected invalid-output classification: %v", err)
	}
}

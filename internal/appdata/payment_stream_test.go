package appdata

import (
	"net/http/httptest"
	"testing"
	"time"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
)

type writeDeadlineRecorder struct {
	*httptest.ResponseRecorder
	deadline time.Time
	calls    int
}

func (r *writeDeadlineRecorder) SetWriteDeadline(deadline time.Time) error {
	r.deadline = deadline
	r.calls++
	return nil
}

func TestDisableResponseWriteDeadlineUnwrapsLoggingWriter(t *testing.T) {
	recorder := &writeDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	wrapped := chimiddleware.NewWrapResponseWriter(recorder, 1)

	if err := disableResponseWriteDeadline(wrapped); err != nil {
		t.Fatalf("disableResponseWriteDeadline() error = %v", err)
	}
	if recorder.calls != 1 {
		t.Fatalf("SetWriteDeadline() calls = %d, want 1", recorder.calls)
	}
	if !recorder.deadline.IsZero() {
		t.Fatalf("write deadline = %v, want zero", recorder.deadline)
	}
}

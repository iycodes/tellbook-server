package appdata

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNormalizeHandleSlug(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "business name", input: " Zenith Lash Studio ", want: "zenith-lash-studio"},
		{name: "symbols collapse", input: "Kisha's  Beauty & Spa", want: "kisha-s-beauty-spa"},
		{name: "numbers remain", input: "Studio 24/7", want: "studio-24-7"},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, err := normalizeHandleSlug(test.input)
			if err != nil {
				t.Fatalf("normalizeHandleSlug() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("normalizeHandleSlug() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestNormalizeHandleSlugRejectsInvalidValues(t *testing.T) {
	t.Parallel()

	tests := []string{
		"---",
		"   ",
		"abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklm",
	}

	for _, input := range tests {
		if _, err := normalizeHandleSlug(input); !errors.Is(err, ErrInvalidHandleSlug) {
			t.Fatalf("normalizeHandleSlug(%q) error = %v, want ErrInvalidHandleSlug", input, err)
		}
	}
}

func TestWriteProfileUpdateError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{name: "not found", err: ErrNotFound, wantStatus: http.StatusNotFound, wantCode: "profile_not_found"},
		{name: "invalid handle", err: ErrInvalidHandleSlug, wantStatus: http.StatusBadRequest, wantCode: "invalid_handle_slug"},
		{name: "taken handle", err: ErrHandleSlugTaken, wantStatus: http.StatusConflict, wantCode: "handle_slug_taken"},
		{name: "unexpected", err: errors.New("boom"), wantStatus: http.StatusInternalServerError, wantCode: "profile_update_failed"},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			recorder := httptest.NewRecorder()
			writeProfileUpdateError(recorder, test.err)

			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, test.wantStatus)
			}

			var payload struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if payload.Error.Code != test.wantCode {
				t.Fatalf("error code = %q, want %q", payload.Error.Code, test.wantCode)
			}
		})
	}
}

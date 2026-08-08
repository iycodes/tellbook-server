package auth

import (
	"bytes"
	"testing"
)

func TestNewSixDigitCode(t *testing.T) {
	for range 100 {
		code, tokenHash, err := newSixDigitCode()
		if err != nil {
			t.Fatalf("newSixDigitCode() error = %v", err)
		}
		if !isSixDigitCode(code) {
			t.Fatalf("newSixDigitCode() code = %q, want six numeric digits", code)
		}
		if !bytes.Equal(tokenHash, hashToken(code)) {
			t.Fatal("newSixDigitCode() returned a hash that does not match the code")
		}
	}
}

func TestIsSixDigitCode(t *testing.T) {
	tests := []struct {
		value string
		want  bool
	}{
		{value: "000000", want: true},
		{value: "123456", want: true},
		{value: "12345", want: false},
		{value: "1234567", want: false},
		{value: "12345a", want: false},
		{value: "１２３４５６", want: false},
	}

	for _, test := range tests {
		if got := isSixDigitCode(test.value); got != test.want {
			t.Errorf("isSixDigitCode(%q) = %t, want %t", test.value, got, test.want)
		}
	}
}

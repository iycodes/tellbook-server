package appdata

import (
	"errors"
	"testing"
)

func TestValidatePublicBookingContact(t *testing.T) {
	tests := []struct {
		name  string
		input CreatePublicBookingQuoteInput
		valid bool
	}{
		{name: "international phone", input: CreatePublicBookingQuoteInput{CustomerName: "Ada", CustomerEmail: "Ada@Example.com", CustomerPhone: "+234 803 123 4567"}, valid: true},
		{name: "invalid email", input: CreatePublicBookingQuoteInput{CustomerName: "Ada", CustomerEmail: "not-an-email", CustomerPhone: "+2348031234567"}},
		{name: "invalid phone", input: CreatePublicBookingQuoteInput{CustomerName: "Ada", CustomerEmail: "ada@example.com", CustomerPhone: "call-me"}},
		{name: "missing name", input: CreatePublicBookingQuoteInput{CustomerEmail: "ada@example.com", CustomerPhone: "+2348031234567"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			email, err := validatePublicBookingContact(test.input)
			if test.valid {
				if err != nil || email != "ada@example.com" {
					t.Fatalf("validate contact = %q, %v", email, err)
				}
				return
			}
			if !errors.Is(err, ErrInvalidContact) {
				t.Fatalf("validate contact error = %v, want ErrInvalidContact", err)
			}
		})
	}
}

package appdata

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestInboxModerationValidation(t *testing.T) {
	repo := &Repository{}
	for _, test := range []struct {
		name           string
		conversationID uuid.UUID
		reason         string
		operator       string
	}{
		{name: "missing conversation", reason: "abuse report", operator: "operator@example.com"},
		{name: "missing reason", conversationID: uuid.New(), operator: "operator@example.com"},
		{name: "missing operator", conversationID: uuid.New(), reason: "abuse report"},
		{name: "long reason", conversationID: uuid.New(), reason: strings.Repeat("a", 501), operator: "operator@example.com"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := repo.SetInboxConversationDisabled(
				t.Context(), test.conversationID, true, test.reason, test.operator,
			)
			if !errors.Is(err, ErrInboxInvalidModeration) {
				t.Fatalf("error = %v, want invalid moderation", err)
			}
		})
	}
}

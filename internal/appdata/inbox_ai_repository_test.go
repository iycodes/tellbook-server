package appdata

import (
	"strings"
	"testing"

	aiapi "booking/go-server/shared/ai_api"
)

func TestNewestMessagesWithinCharacterLimitKeepsLatestContext(t *testing.T) {
	messages := []aiapi.MessageTurn{
		{Role: "customer", Content: strings.Repeat("a", 5)},
		{Role: "provider", Content: strings.Repeat("b", 5)},
		{Role: "customer", Content: strings.Repeat("c", 5)},
	}

	result := newestMessagesWithinCharacterLimit(messages, 10)
	if len(result) != 2 || result[0].Content != "bbbbb" || result[1].Content != "ccccc" {
		t.Fatalf("unexpected bounded messages: %+v", result)
	}
}

func TestNewestMessagesWithinCharacterLimitTruncatesSingleLatestMessage(t *testing.T) {
	messages := []aiapi.MessageTurn{{Role: "customer", Content: "123456"}}
	result := newestMessagesWithinCharacterLimit(messages, 4)
	if len(result) != 1 || result[0].Content != "1234" {
		t.Fatalf("unexpected truncated message: %+v", result)
	}
}

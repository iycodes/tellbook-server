package whatsapp

import (
	"context"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type TessaInboundMessage struct {
	ReceiptID, ClientID                       uuid.UUID
	PhoneNumberID, Sender, MessageID, Content string
	ConnectionRevision, SecurityRevision      int64
	SourceTimestamp                           time.Time
}
type TessaConversationIngress interface {
	StoreTessaWhatsAppMessageTx(context.Context, pgx.Tx, TessaInboundMessage) error
}

// B3 is exercised through this adapter in controlled tests. Production wiring
// stays closed until B4 can deliver committed assistant answers back to WhatsApp.
func (r *TessaLinkRepository) WithConversationIngress(ingress TessaConversationIngress) *TessaLinkRepository {
	r.conversations = ingress
	return r
}

func tessaLinkingText(text string) bool {
	text = strings.TrimSpace(text)
	upper := strings.ToUpper(text)
	switch upper {
	case "CONNECT", "CREATE", "AGREE", "RESEND", "START", "STOP", "TESSA DISCONNECT":
		return true
	}
	if strings.HasPrefix(upper, "TESSA LINK") || strings.HasPrefix(upper, "VERIFY ") {
		return true
	}
	if len(text) >= 4 && len(text) <= 8 {
		digits := true
		for _, c := range text {
			if c < '0' || c > '9' {
				digits = false
				break
			}
		}
		if digits {
			return true
		}
	}
	if a, err := mail.ParseAddress(text); err == nil && a.Address == text {
		return true
	}
	return false
}

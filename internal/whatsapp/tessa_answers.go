package whatsapp

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// EnqueueTessaAnswerTx must share the answer/terminal-run transaction. It stores
// references, not a second copy of the answer or a request to rerun inference.
func EnqueueTessaAnswerTx(ctx context.Context, tx pgx.Tx, clientID, threadID, runID, messageID uuid.UUID, notice string) error {
	tag, err := tx.Exec(ctx, `INSERT INTO tessa_whatsapp_outbox(id,source_receipt_id,client_id,phone_number_id,destination,
      connection_revision,security_revision,kind,window_expires_at,assistant_message_id,thread_id,core_notice_revision)
      SELECT gen_random_uuid(),i.source_receipt_id,i.client_id,i.phone_number_id,i.destination,i.connection_revision,
      i.security_revision,'answer',i.source_timestamp+INTERVAL '24 hours',m.id,i.thread_id,$5
      FROM tessa_whatsapp_ingress i JOIN tessa_messages m ON m.id=$4 AND m.client_id=i.client_id AND m.thread_id=i.thread_id
      AND m.run_id=i.run_id AND m.sender_type='tessa' AND m.source_channel='whatsapp'
      WHERE i.client_id=$1 AND i.thread_id=$2 AND i.run_id=$3 AND i.status='admitted'`, clientID, threadID, runID, messageID, notice)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("Tessa answer has no authorized ingress reference")
	}
	_, err = tx.Exec(ctx, `SELECT pg_notify('tellbook_worker_core','tessa_whatsapp')`)
	return err
}

// Set only together with the ingress/admission wiring when delivery UI is ready.
func (r *TessaLinkRepository) WithAssistantReplies(webURL, coreNotice string) error {
	u, err := url.Parse(webURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || strings.TrimSpace(coreNotice) == "" {
		return errors.New("Tessa WhatsApp replies require an HTTPS provider origin and current core notice")
	}
	r.answerOrigin = strings.TrimRight(u.String(), "/")
	r.answerNotice = coreNotice
	return nil
}

type renderedTessaAnswer struct {
	Body   string
	Button *URLButton
}

func renderTessaAnswer(content string, presentation []byte, origin string) (renderedTessaAnswer, error) {
	content = strings.TrimSpace(content)
	if content == "" || !utf8.ValidString(content) || utf8.RuneCountInString(content) > 4000 {
		return renderedTessaAnswer{}, errors.New("invalid committed Tessa answer")
	}
	var p struct {
		Actions []struct {
			RouteID  string `json:"route_id"`
			EntityID string `json:"entity_id"`
		} `json:"actions"`
	}
	if err := json.Unmarshal(presentation, &p); err != nil {
		return renderedTessaAnswer{}, err
	}
	// Presentation actions are committed from safe tool evidence, never model URLs.
	bookings := map[uuid.UUID]bool{}
	bookingList := false
	for _, action := range p.Actions {
		if action.RouteID == "bookings" {
			bookingList = true
		}
		if action.RouteID == "booking_details" {
			if id, err := uuid.Parse(action.EntityID); err == nil && id != uuid.Nil {
				bookings[id] = true
			}
		}
	}
	if bookingList || len(bookings) > 0 {
		if utf8.RuneCountInString(content) > 1024 {
			return renderedTessaAnswer{}, errors.New("committed booking answer exceeds WhatsApp button limit")
		}
		button := &URLButton{Label: "View bookings", URL: origin + "/bookings"}
		if !bookingList && len(bookings) == 1 {
			for id := range bookings {
				button = &URLButton{Label: "View booking", URL: origin + "/bookings?booking=" + id.String()}
			}
		}
		return renderedTessaAnswer{Body: content, Button: button}, nil
	}
	seen := map[string]bool{}
	for n, a := range p.Actions {
		if n >= 3 {
			break
		}
		path, label := "", ""
		switch a.RouteID {
		case "customers":
			path, label = "/clients", "Customers"
		case "customer_details":
			id, err := uuid.Parse(a.EntityID)
			if err != nil || id == uuid.Nil {
				continue
			}
			path, label = "/clients?customer="+id.String(), "Customer details"
		case "inbox":
			path, label = "/inbox", "Inbox"
		// These screens use browser history state for their inner panels. Link to
		// the real parent route, never invent an unsupported URL deep link.
		case "services", "service_details", "payouts", "business_profile":
			path, label = "/settings", "Tellbook settings"
		case "payments", "stats", "reviews":
			path, label = "/", "Tellbook dashboard"
		default:
			continue
		}
		if seen[path] {
			continue
		}
		seen[path] = true
		link := "\n\n" + label + ": " + origin + path
		if utf8.RuneCountInString(content+link) <= 4096 {
			content += link
		}
	}
	return renderedTessaAnswer{Body: content}, nil
}

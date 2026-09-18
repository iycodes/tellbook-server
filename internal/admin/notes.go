package admin

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"strings"
	"time"
)

type Note struct {
	ID        uuid.UUID `json:"id"`
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}
type NotePage struct {
	Items      []Note `json:"items"`
	NextCursor string `json:"next_cursor"`
}

// This allowlist is deliberately limited to supported investigation records.
// SQL identifiers never come from request text.
func noteTarget(kind string) (column, lookup, capability string, err error) {
	switch kind {
	case "support_case":
		return "case_id", `SELECT id FROM admin_support_cases WHERE id=$1`, "support.manage", nil
	case "business":
		return "business_id", `SELECT client_id FROM client_profiles WHERE client_id=$1`, "businesses.note", nil
	case "booking":
		return "booking_id", `SELECT id FROM bookings WHERE id=$1`, "bookings.note", nil
	case "customer_contact":
		return "contact_id", `SELECT id FROM customers WHERE id=$1`, "customers.note", nil
	case "marketplace_account":
		return "account_id", `SELECT id FROM marketplace_customers WHERE id=$1`, "customers.note", nil
	default:
		return "", "", "", problem(404, "not_found", "Note target not found.")
	}
}

func (s *Service) RecordNotes(ctx context.Context, kind string, id uuid.UUID, cursor *uuid.UUID) (NotePage, error) {
	out := NotePage{Items: []Note{}}
	column, lookup, _, e := noteTarget(kind)
	if e != nil {
		return out, e
	}
	var target uuid.UUID
	if e = s.db.QueryRow(ctx, lookup, id).Scan(&target); errors.Is(e, pgx.ErrNoRows) {
		return out, problem(404, "not_found", "Note target not found.")
	}
	if e != nil {
		return out, e
	}
	if cursor != nil {
		var found bool
		if e = s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM admin_business_notes WHERE id=$1 AND `+column+`=$2)`, cursor, id).Scan(&found); e != nil {
			return out, e
		}
		if !found {
			return out, problem(422, "invalid_cursor", "This note page belongs to another record. Return to the latest notes.")
		}
	}
	rows, e := s.db.Query(ctx, `SELECT n.id,p.full_name,n.body,n.created_at FROM admin_business_notes n JOIN admin_staff p ON p.id=n.author_id WHERE n.`+column+`=$1 AND ($2::uuid IS NULL OR (n.created_at,n.id)<(SELECT created_at,id FROM admin_business_notes WHERE id=$2 AND `+column+`=$1)) ORDER BY n.created_at DESC,n.id DESC LIMIT 26`, id, cursor)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	for rows.Next() {
		var n Note
		if e = rows.Scan(&n.ID, &n.Author, &n.Body, &n.CreatedAt); e != nil {
			return out, e
		}
		out.Items = append(out.Items, n)
	}
	if len(out.Items) > 25 {
		out.Items = out.Items[:25]
		out.NextCursor = out.Items[24].ID.String()
	}
	return out, rows.Err()
}

func (s *Service) AddRecordNote(ctx context.Context, session Session, kind string, id, key uuid.UUID, body string) (Note, error) {
	out := Note{}
	column, lookup, capability, e := noteTarget(kind)
	if e != nil {
		return out, e
	}
	body = strings.TrimSpace(body)
	if len([]rune(body)) < 1 || len([]rune(body)) > 4000 {
		return out, problem(422, "invalid_body", "Write a note between 1 and 4,000 characters.")
	}
	if key == uuid.Nil {
		return out, problem(422, "invalid_request_key", "A request key is required.")
	}
	tx, e := s.db.Begin(ctx)
	if e != nil {
		return out, e
	}
	defer tx.Rollback(ctx)
	if e = s.authorizeTx(ctx, tx, session, capability); e != nil {
		return out, e
	}
	var target uuid.UUID
	if e = tx.QueryRow(ctx, lookup+` FOR KEY SHARE`, id).Scan(&target); errors.Is(e, pgx.ErrNoRows) {
		return out, problem(404, "not_found", "Note target not found.")
	}
	if e != nil {
		return out, e
	}
	out.ID = uuid.New()
	tag, e := tx.Exec(ctx, `INSERT INTO admin_business_notes(id,`+column+`,author_id,body,request_key) VALUES($1,$2,$3,$4,$5) ON CONFLICT(author_id,request_key) DO NOTHING`, out.ID, id, session.Staff.ID, body, key)
	if e != nil {
		return out, e
	}
	var savedTarget *uuid.UUID
	e = tx.QueryRow(ctx, `SELECT id,`+column+`,body,created_at FROM admin_business_notes WHERE author_id=$1 AND request_key=$2`, session.Staff.ID, key).Scan(&out.ID, &savedTarget, &out.Body, &out.CreatedAt)
	if e != nil {
		return out, e
	}
	if savedTarget == nil || *savedTarget != id || out.Body != body {
		return out, problem(409, "idempotency_conflict", "This request key was used for a different note. Refresh before submitting a new note.")
	}
	out.Author = session.Staff.Name
	if tag.RowsAffected() == 1 {
		if e = audit(ctx, tx, &session.Staff.ID, kind+".note_added", kind, &id, "Internal investigation note", map[string]any{"note_id": out.ID}); e != nil {
			return out, e
		}
	}
	return out, tx.Commit(ctx)
}

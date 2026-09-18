package admin

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type CustomerRecord struct {
	ID           uuid.UUID  `json:"id"`
	Kind         string     `json:"kind"`
	Name         string     `json:"name"`
	Email        string     `json:"email"`
	Phone        string     `json:"phone"`
	BusinessID   *uuid.UUID `json:"business_id"`
	BusinessName string     `json:"business_name"`
	CreatedAt    time.Time  `json:"created_at"`
}

// Directory and detail share an explicit safe projection. Provider-owned private
// notes and account authentication fields are never selected for staff reads.
func customerSelect(kind string) (string, error) {
	switch kind {
	case "contacts":
		return `SELECT c.id,c.full_name,c.email,c.phone,c.client_id,p.business_name,c.created_at FROM customers c JOIN client_profiles p ON p.client_id=c.client_id`, nil
	case "accounts":
		return `SELECT c.id,c.full_name,coalesce(c.email,''),coalesce(c.phone_e164,''),NULL::uuid,''::text,c.created_at FROM marketplace_customers c`, nil
	default:
		return "", problem(404, "not_found", "Customer record type not found.")
	}
}

func scanCustomer(row pgx.Row, kind string) (CustomerRecord, error) {
	out := CustomerRecord{Kind: kind}
	e := row.Scan(&out.ID, &out.Name, &out.Email, &out.Phone, &out.BusinessID, &out.BusinessName, &out.CreatedAt)
	out.CreatedAt = out.CreatedAt.UTC()
	return out, e
}

func (s *Service) CustomerRecord(ctx context.Context, kind string, id uuid.UUID) (CustomerRecord, error) {
	query, e := customerSelect(kind)
	if e != nil {
		return CustomerRecord{}, e
	}
	out, e := scanCustomer(s.db.QueryRow(ctx, query+` WHERE c.id=$1`, id), kind)
	if errors.Is(e, pgx.ErrNoRows) {
		return out, problem(404, "not_found", "Customer record not found.")
	}
	return out, e
}

type CustomerFilter struct {
	Kind, Q, Cursor string
	BusinessID      *uuid.UUID
}

type CustomerPage struct {
	Items      []CustomerRecord `json:"items"`
	NextCursor string           `json:"next_cursor"`
}

type customerCursor struct {
	ID    uuid.UUID `json:"id"`
	Scope string    `json:"scope"`
}

func (s *Service) Customers(ctx context.Context, f CustomerFilter) (CustomerPage, error) {
	out := CustomerPage{Items: []CustomerRecord{}}
	query, e := customerSelect(f.Kind)
	if e != nil {
		return out, e
	}
	f.Q = strings.TrimSpace(f.Q)
	if len([]rune(f.Q)) > 100 {
		return out, problem(422, "invalid_search", "Search up to 100 characters.")
	}
	if f.Kind == "accounts" && f.BusinessID != nil {
		return out, problem(422, "invalid_scope", "A business scope applies only to business contacts.")
	}
	fingerprint, _ := json.Marshal([]any{f.Kind, f.Q, f.BusinessID})
	scope := fmt.Sprintf("%x", sha256.Sum256(fingerprint))
	var after any
	if f.Cursor != "" {
		if len(f.Cursor) > 500 {
			return out, problem(422, "invalid_cursor", "Invalid customer page cursor.")
		}
		var c customerCursor
		raw, err := base64.RawURLEncoding.DecodeString(f.Cursor)
		if err != nil || json.Unmarshal(raw, &c) != nil || c.ID == uuid.Nil || c.Scope != scope {
			return out, problem(422, "invalid_cursor", "This page belongs to different filters. Start from the first page.")
		}
		after = c.ID
	}
	// Literal substring matching: wildcard characters in a name/email must not
	// turn into a broader query. Existing lower(name) trigram indexes are reused.
	pattern := ""
	if f.Q != "" {
		pattern = "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(strings.ToLower(f.Q)) + "%"
	}
	phone := "c.phone"
	if f.Kind == "accounts" {
		phone = "c.phone_e164"
	}
	query += ` WHERE ($1='' OR lower(c.full_name) LIKE $1 ESCAPE '\' OR lower(c.email) LIKE $1 ESCAPE '\' OR ` + phone + ` LIKE $1 ESCAPE '\' OR c.id::text=$4)
 AND ($2::uuid IS NULL OR c.id>$2)`
	args := []any{pattern, after, f.BusinessID, f.Q}
	if f.Kind == "contacts" {
		query += ` AND ($3::uuid IS NULL OR c.client_id=$3)`
	} else {
		query += ` AND $3::uuid IS NULL`
	}
	rows, e := s.db.Query(ctx, query+` ORDER BY c.id LIMIT 26`, args...)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	for rows.Next() {
		c, err := scanCustomer(rows, f.Kind)
		if err != nil {
			return out, err
		}
		out.Items = append(out.Items, c)
	}
	if e = rows.Err(); e != nil {
		return out, e
	}
	if len(out.Items) > 25 {
		out.Items = out.Items[:25]
		raw, _ := json.Marshal(customerCursor{out.Items[24].ID, scope})
		out.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	return out, nil
}

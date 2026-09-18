package admin

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"strings"
	"time"
)

type Business struct {
	PlatformRestricted bool      `json:"platform_restricted"`
	UpdatedAt          time.Time `json:"updated_at"`
	ID                 uuid.UUID `json:"id"`
	Name               string    `json:"name"`
	Handle             string    `json:"handle"`
	Category           string    `json:"category"`
	City               string    `json:"city"`
	Country            string    `json:"country"`
	Currency           string    `json:"currency"`
	Timezone           string    `json:"timezone"`
	Verified           bool      `json:"verified"`
	MarketplaceEnabled bool      `json:"marketplace_enabled"`
	OwnerName          string    `json:"owner_name"`
	OwnerEmail         string    `json:"owner_email"`
	CreatedAt          time.Time `json:"created_at"`
}

const businessColumns = `c.id,p.business_name,p.handle_slug,p.category,p.city,coalesce(p.country_code,''),coalesce(p.currency_code,''),coalesce(p.timezone,''),p.verified,p.marketplace_enabled,c.full_name,coalesce(c.email,''),c.created_at,p.platform_restricted,p.updated_at`

func scanBusiness(row pgx.Row) (Business, error) {
	var b Business
	e := row.Scan(&b.ID, &b.Name, &b.Handle, &b.Category, &b.City, &b.Country, &b.Currency, &b.Timezone, &b.Verified, &b.MarketplaceEnabled, &b.OwnerName, &b.OwnerEmail, &b.CreatedAt, &b.PlatformRestricted, &b.UpdatedAt)
	return b, e
}

type BusinessPage struct {
	Items      []Business `json:"items"`
	NextCursor string     `json:"next_cursor"`
}

func (s *Service) Businesses(ctx context.Context, q, verification string, cursor *uuid.UUID) (BusinessPage, error) {
	out := BusinessPage{Items: []Business{}}
	q = strings.TrimSpace(q)
	if len([]rune(q)) > 100 {
		return out, problem(422, "invalid_search", "Search up to 100 characters.")
	}
	if verification != "" && verification != "verified" && verification != "unverified" {
		return out, problem(422, "invalid_filter", "Choose a valid verification filter.")
	}
	rows, e := s.db.Query(ctx, `SELECT `+businessColumns+` FROM clients c JOIN client_profiles p ON p.client_id=c.id WHERE ($1='' OR lower(p.business_name) LIKE '%'||$1||'%' OR lower(p.handle_slug) LIKE '%'||$1||'%') AND ($2='' OR p.verified=($2='verified')) AND ($3::uuid IS NULL OR c.id>$3) ORDER BY c.id LIMIT 26`, strings.ToLower(q), verification, cursor)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	for rows.Next() {
		b, e := scanBusiness(rows)
		if e != nil {
			return out, e
		}
		out.Items = append(out.Items, b)
	}
	if len(out.Items) > 25 {
		out.Items = out.Items[:25]
		out.NextCursor = out.Items[24].ID.String()
	}
	return out, rows.Err()
}
func (s *Service) Business(ctx context.Context, id uuid.UUID) (Business, error) {
	b, e := scanBusiness(s.db.QueryRow(ctx, `SELECT `+businessColumns+` FROM clients c JOIN client_profiles p ON p.client_id=c.id WHERE c.id=$1`, id))
	if errors.Is(e, pgx.ErrNoRows) {
		return b, problem(404, "not_found", "Business not found.")
	}
	return b, e
}

// Business routes remain compatible while all staff notes share one owner.
func (s *Service) Notes(ctx context.Context, id uuid.UUID, cursor *uuid.UUID) (NotePage, error) {
	return s.RecordNotes(ctx, "business", id, cursor)
}
func (s *Service) AddNote(ctx context.Context, session Session, id, key uuid.UUID, body string) (Note, error) {
	return s.AddRecordNote(ctx, session, "business", id, key, body)
}

type AuditEvent struct {
	Details    json.RawMessage `json:"details"`
	ID         uuid.UUID       `json:"id"`
	Actor      string          `json:"actor"`
	Action     string          `json:"action"`
	EntityType string          `json:"entity_type"`
	EntityID   *uuid.UUID      `json:"entity_id"`
	Reason     string          `json:"reason"`
	RequestID  string          `json:"request_id"`
	CreatedAt  time.Time       `json:"created_at"`
}
type AuditPage struct {
	Items      []AuditEvent `json:"items"`
	NextCursor string       `json:"next_cursor"`
}

func (s *Service) Audit(ctx context.Context, businessID, cursor *uuid.UUID) (AuditPage, error) {
	out := AuditPage{Items: []AuditEvent{}}
	rows, e := s.db.Query(ctx, `SELECT a.id,coalesce(p.full_name,CASE WHEN a.action IN ('staff.bootstrapped','staff.bootstrap_invitation_renewed') THEN 'Operational command' WHEN a.action='staff.password_reset_requested' THEN 'Unauthenticated request' ELSE 'System' END),a.action,a.entity_type,a.entity_id,a.reason,a.request_id,a.created_at,a.details FROM admin_audit_events a LEFT JOIN admin_staff p ON p.id=a.actor_id WHERE ($1::uuid IS NULL OR (a.entity_type='business' AND a.entity_id=$1)) AND ($2::uuid IS NULL OR (a.created_at,a.id)<(SELECT created_at,id FROM admin_audit_events WHERE id=$2)) ORDER BY a.created_at DESC,a.id DESC LIMIT 26`, businessID, cursor)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	for rows.Next() {
		var a AuditEvent
		if e = rows.Scan(&a.ID, &a.Actor, &a.Action, &a.EntityType, &a.EntityID, &a.Reason, &a.RequestID, &a.CreatedAt, &a.Details); e != nil {
			return out, e
		}
		out.Items = append(out.Items, a)
	}
	if len(out.Items) > 25 {
		out.Items = out.Items[:25]
		out.NextCursor = out.Items[24].ID.String()
	}
	return out, rows.Err()
}

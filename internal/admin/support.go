package admin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type SupportCase struct {
	ID           uuid.UUID  `json:"id"`
	Title        string     `json:"title"`
	Description  string     `json:"description"`
	Status       string     `json:"status"`
	Priority     string     `json:"priority"`
	AssigneeID   *uuid.UUID `json:"assignee_id"`
	AssigneeName string     `json:"assignee_name"`
	FollowUp     string     `json:"follow_up"`
	Resolution   string     `json:"resolution"`
	Revision     int        `json:"revision"`
	TargetKind   string     `json:"target_kind"`
	TargetID     *uuid.UUID `json:"target_id"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}
type SupportPage struct {
	Items      []SupportCase `json:"items"`
	NextCursor string        `json:"next_cursor"`
	Today      string        `json:"today"`
}
type SupportFilter struct{ Q, Status, Priority, Owner, Due, Cursor string }
type SupportChange struct {
	Actor     string    `json:"actor"`
	Action    string    `json:"action"`
	Reason    string    `json:"reason"`
	Revision  int       `json:"revision"`
	CreatedAt time.Time `json:"created_at"`
}
type SupportDetail struct {
	SupportCase
	History     []SupportChange `json:"history"`
	MoreHistory bool            `json:"more_history"`
}
type SupportInput struct {
	Title            string     `json:"title"`
	Description      string     `json:"description"`
	Status           string     `json:"status"`
	Priority         string     `json:"priority"`
	AssigneeID       *uuid.UUID `json:"assignee_id"`
	FollowUp         string     `json:"follow_up"`
	TargetKind       string     `json:"target_kind"`
	TargetID         *uuid.UUID `json:"target_id"`
	Reason           string     `json:"reason"`
	ExpectedRevision int        `json:"expected_revision"`
	RequestKey       uuid.UUID  `json:"request_key"`
}
type SupportResult struct {
	ID       uuid.UUID `json:"id"`
	Revision int       `json:"revision"`
}

const supportSelect = `SELECT c.id,c.title,c.description,c.status,c.priority,c.assignee_id,coalesce(a.full_name,''),coalesce(c.follow_up::text,''),c.resolution,c.revision,
 CASE WHEN c.business_id IS NOT NULL THEN 'business' WHEN c.booking_id IS NOT NULL THEN 'booking' WHEN c.contact_id IS NOT NULL THEN 'customer_contact' WHEN c.account_id IS NOT NULL THEN 'marketplace_account' ELSE '' END,
 coalesce(c.business_id,c.booking_id,c.contact_id,c.account_id),c.created_at,c.updated_at FROM admin_support_cases c LEFT JOIN admin_staff a ON a.id=c.assignee_id`

func scanSupport(row pgx.Row) (SupportCase, error) {
	var c SupportCase
	e := row.Scan(&c.ID, &c.Title, &c.Description, &c.Status, &c.Priority, &c.AssigneeID, &c.AssigneeName, &c.FollowUp, &c.Resolution, &c.Revision, &c.TargetKind, &c.TargetID, &c.CreatedAt, &c.UpdatedAt)
	return c, e
}
func (s *Service) SupportCases(ctx context.Context, actor uuid.UUID, f SupportFilter) (SupportPage, error) {
	out := SupportPage{Items: []SupportCase{}, Today: s.now().UTC().Format(time.DateOnly)}
	f.Q = strings.TrimSpace(f.Q)
	if len([]rune(f.Q)) > 100 || !slices.Contains([]string{"", "active", "open", "waiting", "resolved"}, f.Status) || !slices.Contains([]string{"", "low", "normal", "high", "urgent"}, f.Priority) || !slices.Contains([]string{"", "me", "unassigned"}, f.Owner) || !slices.Contains([]string{"", "due"}, f.Due) {
		return out, problem(422, "invalid_filters", "Choose valid case filters and search up to 100 characters.")
	}
	raw, _ := json.Marshal([]any{"support", actor, f.Q, f.Status, f.Priority, f.Owner, f.Due, out.Today})
	scope := fmt.Sprintf("%x", sha256.Sum256(raw))
	var after, afterID any
	if f.Cursor != "" {
		var c financeCursor
		raw, e := base64.RawURLEncoding.DecodeString(f.Cursor)
		if e != nil || len(raw) > 500 || json.Unmarshal(raw, &c) != nil || c.Scope != scope || c.ID == uuid.Nil || c.At.IsZero() {
			return out, problem(422, "invalid_cursor", "This page belongs to different filters or a previous day. Start from the first page.")
		}
		after, afterID = c.At, c.ID
	}
	rows, e := s.db.Query(ctx, supportSelect+` WHERE ($1='' OR strpos(lower(c.title),lower($1))>0 OR c.id::text=$1)
 AND ($2='' OR c.status=$2 OR ($2='active' AND c.status<>'resolved')) AND ($3='' OR c.priority=$3)
 AND ($4='' OR ($4='me' AND c.assignee_id=$5) OR ($4='unassigned' AND c.assignee_id IS NULL))
 AND ($6='' OR (c.status<>'resolved' AND c.follow_up<=$7::date))
 AND ($8::timestamptz IS NULL OR (c.created_at,c.id)<($8,$9::uuid)) ORDER BY c.created_at DESC,c.id DESC LIMIT 26`, f.Q, f.Status, f.Priority, f.Owner, actor, f.Due, out.Today, after, afterID)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	for rows.Next() {
		c, e := scanSupport(rows)
		if e != nil {
			return out, e
		}
		out.Items = append(out.Items, c)
	}
	if e = rows.Err(); e != nil {
		return out, e
	}
	if len(out.Items) > 25 {
		out.Items = out.Items[:25]
		last := out.Items[24]
		out.NextCursor = financeNextCursor(last.CreatedAt, last.ID, scope)
	}
	return out, nil
}
func (s *Service) SupportCase(ctx context.Context, id uuid.UUID) (SupportDetail, error) {
	out := SupportDetail{History: []SupportChange{}}
	// A single read snapshot keeps the displayed version and history consistent.
	tx, e := s.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return out, e
	}
	defer tx.Rollback(ctx)
	out.SupportCase, e = scanSupport(tx.QueryRow(ctx, supportSelect+` WHERE c.id=$1`, id))
	if errors.Is(e, pgx.ErrNoRows) {
		return out, problem(404, "not_found", "Support case not found.")
	}
	if e != nil {
		return out, e
	}
	rows, e := tx.Query(ctx, `SELECT s.full_name,h.action,h.reason,h.revision,h.created_at FROM admin_support_changes h JOIN admin_staff s ON s.id=h.actor_id WHERE h.case_id=$1 ORDER BY h.revision DESC LIMIT 26`, id)
	if e != nil {
		return out, e
	}
	for rows.Next() {
		var h SupportChange
		if e = rows.Scan(&h.Actor, &h.Action, &h.Reason, &h.Revision, &h.CreatedAt); e != nil {
			rows.Close()
			return out, e
		}
		out.History = append(out.History, h)
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return out, e
	}
	if len(out.History) > 25 {
		out.History = out.History[:25]
		out.MoreHistory = true
	}
	return out, tx.Commit(ctx)
}
func (s *Service) SaveSupportCase(ctx context.Context, session Session, id uuid.UUID, in SupportInput) (SupportResult, error) {
	var out SupportResult
	in.Title = strings.TrimSpace(in.Title)
	in.Description = strings.TrimSpace(in.Description)
	in.Reason = strings.TrimSpace(in.Reason)
	for _, f := range []struct {
		name, value string
		max         int
	}{{"title", in.Title, 160}, {"description", in.Description, 4000}, {"reason", in.Reason, 1000}} {
		if len([]rune(f.value)) < 1 || len([]rune(f.value)) > f.max {
			return out, problem(422, "invalid_"+f.name, fmt.Sprintf("Enter %s between 1 and %d characters.", f.name, f.max))
		}
	}
	if !slices.Contains([]string{"open", "waiting", "resolved"}, in.Status) {
		return out, problem(422, "invalid_status", "Choose a valid case status.")
	}
	if !slices.Contains([]string{"low", "normal", "high", "urgent"}, in.Priority) {
		return out, problem(422, "invalid_priority", "Choose a valid priority.")
	}
	if in.RequestKey == uuid.Nil || (id != uuid.Nil && in.ExpectedRevision < 1) || (id == uuid.Nil && (in.ExpectedRevision != 0 || in.Status != "open")) {
		return out, problem(422, "invalid_request", "New cases must be open. Reload before updating an existing case.")
	}
	if in.FollowUp != "" {
		d, e := time.Parse(time.DateOnly, in.FollowUp)
		if e != nil || d.Year() < 2000 || d.Year() > 9999 {
			return out, problem(422, "invalid_follow_up", "Enter a follow-up date between 2000 and 9999, or leave it empty.")
		}
	}
	if (in.TargetKind == "") != (in.TargetID == nil) || (in.TargetID != nil && *in.TargetID == uuid.Nil) {
		return out, problem(422, "invalid_target", "Choose both a record type and an existing record ID, or leave both empty.")
	}
	var column, lookup string
	if in.TargetKind != "" {
		if !slices.Contains([]string{"business", "booking", "customer_contact", "marketplace_account"}, in.TargetKind) {
			return out, problem(422, "invalid_target", "Choose a supported linked record.")
		}
		column, lookup, _, _ = noteTarget(in.TargetKind)
	}
	tx, e := s.db.Begin(ctx)
	if e != nil {
		return out, e
	}
	defer tx.Rollback(ctx)
	if e = s.authorizeTx(ctx, tx, session, "support.manage"); e != nil {
		return out, e
	}
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, session.Staff.ID.String()+":"+in.RequestKey.String()); e != nil {
		return out, e
	}
	raw, _ := json.Marshal([]any{id, in})
	fingerprint := sha256.Sum256(raw)
	var saved []byte
	e = tx.QueryRow(ctx, `SELECT case_id,revision,fingerprint FROM admin_support_changes WHERE actor_id=$1 AND request_key=$2`, session.Staff.ID, in.RequestKey).Scan(&out.ID, &out.Revision, &saved)
	if e == nil {
		if !bytes.Equal(saved, fingerprint[:]) {
			return out, problem(409, "idempotency_conflict", "This request key was used for another case change. Reload before submitting.")
		}
		return out, tx.Commit(ctx)
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return out, e
	}
	var before SupportCase
	if id != uuid.Nil {
		before, e = scanSupport(tx.QueryRow(ctx, supportSelect+` WHERE c.id=$1 FOR UPDATE OF c`, id))
		if errors.Is(e, pgx.ErrNoRows) {
			return out, problem(404, "not_found", "Support case not found.")
		}
		if e != nil {
			return out, e
		}
		if before.Revision != in.ExpectedRevision {
			return out, conflict
		}
		if before.TargetKind != in.TargetKind || !sameUUID(before.TargetID, in.TargetID) {
			return out, problem(422, "invalid_target", "The original linked record cannot be changed. Create a separate case if needed.")
		}
	}
	if in.AssigneeID != nil {
		var role, status string
		e = tx.QueryRow(ctx, `SELECT role,status FROM admin_staff WHERE id=$1 FOR SHARE`, in.AssigneeID).Scan(&role, &status)
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return out, e
		}
		// Preserve a former owner's historical assignment; only new assignments must be eligible now.
		if (e != nil || status != "active" || !allowed(role, "support.manage")) && !sameUUID(before.AssigneeID, in.AssigneeID) {
			return out, problem(422, "invalid_assignee", "Assign an active Support or Super Admin staff member.")
		}
	}
	action, resolution := "updated", before.Resolution
	if id == uuid.Nil {
		id = uuid.New()
		action = "created"
		if lookup != "" {
			var target uuid.UUID
			if e = tx.QueryRow(ctx, lookup+` FOR KEY SHARE`, in.TargetID).Scan(&target); errors.Is(e, pgx.ErrNoRows) {
				return out, problem(422, "invalid_target", "Linked record not found.")
			}
			if e != nil {
				return out, e
			}
		}
		query := `INSERT INTO admin_support_cases(id,title,description,status,priority,assignee_id,follow_up`
		args := []any{id, in.Title, in.Description, in.Status, in.Priority, in.AssigneeID, in.FollowUp}
		values := ` VALUES($1,$2,$3,$4,$5,$6,NULLIF($7,'')::date`
		if column != "" {
			query += "," + column
			values += ",$8"
			args = append(args, in.TargetID)
		}
		e = tx.QueryRow(ctx, query+")"+values+`) RETURNING revision`, args...).Scan(&out.Revision)
	} else {
		if before.Status == "resolved" && in.Status != "resolved" {
			action = "reopened"
			resolution = ""
		}
		if before.Status != "resolved" && in.Status == "resolved" {
			action = "resolved"
			resolution = in.Reason
		}
		e = tx.QueryRow(ctx, `UPDATE admin_support_cases SET title=$2,description=$3,status=$4,priority=$5,assignee_id=$6,follow_up=NULLIF($7,'')::date,resolution=$8,revision=revision+1,updated_at=clock_timestamp() WHERE id=$1 RETURNING revision`, id, in.Title, in.Description, in.Status, in.Priority, in.AssigneeID, in.FollowUp, resolution).Scan(&out.Revision)
	}
	if e != nil {
		return out, e
	}
	out.ID = id
	if _, e = tx.Exec(ctx, `INSERT INTO admin_support_changes(actor_id,request_key,case_id,fingerprint,revision,action,reason) VALUES($1,$2,$3,$4,$5,$6,$7)`, session.Staff.ID, in.RequestKey, id, fingerprint[:], out.Revision, action, in.Reason); e != nil {
		return out, e
	}
	if e = audit(ctx, tx, &session.Staff.ID, "support_case."+action, "support_case", &id, in.Reason, map[string]any{"revision": out.Revision, "status": in.Status, "priority": in.Priority, "assignee_id": in.AssigneeID, "follow_up": in.FollowUp}); e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}
func sameUUID(a, b *uuid.UUID) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }

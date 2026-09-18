package admin

import (
	"context"
	"errors"
	"strings"
	"time"

	"booking/go-server/internal/appdata"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type BusinessDetail struct {
	Business
	Readiness appdata.MarketplaceReadiness `json:"readiness"`
}

func (s *Service) BusinessDetail(ctx context.Context, id uuid.UUID) (BusinessDetail, error) {
	b, e := s.Business(ctx, id)
	if e != nil {
		return BusinessDetail{}, e
	}
	settings, e := appdata.NewRepository(s.db).GetMarketplaceProfileSettings(ctx, id)
	return BusinessDetail{Business: b, Readiness: settings.Readiness}, e
}

type BusinessDecision struct {
	Action            string    `json:"action"`
	Reason            string    `json:"reason"`
	Evidence          string    `json:"evidence"`
	RequestKey        uuid.UUID `json:"request_key"`
	ExpectedUpdatedAt time.Time `json:"expected_updated_at"`
}

func (s *Service) DecideBusiness(ctx context.Context, session Session, id uuid.UUID, input BusinessDecision) error {
	input.Reason = strings.TrimSpace(input.Reason)
	input.Evidence = strings.TrimSpace(input.Evidence)
	if input.Action != "verify" && input.Action != "reject" && input.Action != "restrict" && input.Action != "restore" {
		return problem(422, "invalid_action", "Choose a supported business decision.")
	}
	if len([]rune(input.Reason)) < 1 || len([]rune(input.Reason)) > 1000 {
		return problem(422, "invalid_reason", "Give a reason between 1 and 1,000 characters.")
	}
	if len([]rune(input.Evidence)) > 2000 || ((input.Action == "verify" || input.Action == "reject") && input.Evidence == "") {
		return problem(422, "invalid_evidence", "Describe the evidence reviewed, up to 2,000 characters.")
	}
	if input.RequestKey == uuid.Nil || input.ExpectedUpdatedAt.IsZero() {
		return problem(422, "invalid_request", "Reload this business before making a decision.")
	}
	tx, e := s.db.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = s.authorizeTx(ctx, tx, session, "businesses.manage"); e != nil {
		return e
	}
	// Serialize a staff request key before inspecting its existing result, including
	// accidental reuse against another business. This is a bounded command, not a queue.
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, session.Staff.ID.String()+":"+input.RequestKey.String()); e != nil {
		return e
	}
	var saved BusinessDecision
	var savedBusiness uuid.UUID
	e = tx.QueryRow(ctx, `SELECT business_id,action,reason,evidence,expected_updated_at FROM admin_business_decisions WHERE actor_id=$1 AND request_key=$2`, session.Staff.ID, input.RequestKey).Scan(&savedBusiness, &saved.Action, &saved.Reason, &saved.Evidence, &saved.ExpectedUpdatedAt)
	if e == nil {
		if savedBusiness != id || saved.Action != input.Action || saved.Reason != input.Reason || saved.Evidence != input.Evidence || !saved.ExpectedUpdatedAt.Equal(input.ExpectedUpdatedAt) {
			return problem(409, "idempotency_conflict", "This request key belongs to a different decision. Reload and review the business.")
		}
		return tx.Commit(ctx)
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return e
	}
	var verified, restricted bool
	var updated time.Time
	e = tx.QueryRow(ctx, `SELECT verified,platform_restricted,updated_at FROM client_profiles WHERE client_id=$1 FOR UPDATE`, id).Scan(&verified, &restricted, &updated)
	if errors.Is(e, pgx.ErrNoRows) {
		return problem(404, "not_found", "Business not found.")
	}
	if e != nil {
		return e
	}
	if !updated.Equal(input.ExpectedUpdatedAt) {
		return conflict
	}
	beforeVerified, beforeRestricted := verified, restricted
	switch input.Action {
	case "verify":
		verified = true
	case "reject":
		verified = false
	case "restrict":
		restricted = true
	case "restore":
		restricted = false
	}
	// Never overwrite marketplace_enabled: restoration respects the owner's choice.
	if _, e = tx.Exec(ctx, `UPDATE client_profiles SET verified=$2,platform_restricted=$3,updated_at=clock_timestamp() WHERE client_id=$1`, id, verified, restricted); e != nil {
		return e
	}
	decisionID := uuid.New()
	if _, e = tx.Exec(ctx, `INSERT INTO admin_business_decisions(id,business_id,actor_id,request_key,action,reason,evidence,expected_updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, decisionID, id, session.Staff.ID, input.RequestKey, input.Action, input.Reason, input.Evidence, input.ExpectedUpdatedAt); e != nil {
		return e
	}
	if e = audit(ctx, tx, &session.Staff.ID, "business."+input.Action, "business", &id, input.Reason, map[string]any{"decision_id": decisionID, "evidence": input.Evidence, "was_verified": beforeVerified, "verified": verified, "was_restricted": beforeRestricted, "restricted": restricted}); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

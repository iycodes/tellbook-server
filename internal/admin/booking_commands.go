package admin

import (
	"booking/go-server/internal/appdata"
	"context"
	"errors"
	"github.com/google/uuid"
	"strings"
	"time"
)

type BookingCommand struct {
	Action            string    `json:"action"`
	Reason            string    `json:"reason"`
	RequestKey        uuid.UUID `json:"request_key"`
	ExpectedUpdatedAt time.Time `json:"expected_updated_at"`
}
type BookingCommandResult struct {
	CommandID uuid.UUID `json:"command_id"`
	Status    string    `json:"status"`
}

func (s *Service) CommandBooking(ctx context.Context, session Session, id uuid.UUID, input BookingCommand) (BookingCommandResult, error) {
	out := BookingCommandResult{}
	if input.Action != "confirm" && input.Action != "complete" && input.Action != "mark_no_show" {
		return out, problem(422, "invalid_action", "Choose a supported non-financial booking action.")
	}
	input.Reason = strings.TrimSpace(input.Reason)
	if len([]rune(input.Reason)) < 1 || len([]rune(input.Reason)) > 1000 {
		return out, problem(422, "invalid_reason", "Give a reason between 1 and 1,000 characters.")
	}
	if input.RequestKey == uuid.Nil || input.ExpectedUpdatedAt.IsZero() {
		return out, problem(422, "invalid_command", "Refresh the booking before submitting an action.")
	}
	tx, e := s.db.Begin(ctx)
	if e != nil {
		return out, e
	}
	defer tx.Rollback(ctx)
	if e = s.authorizeTx(ctx, tx, session, "bookings.manage"); e != nil {
		return out, e
	}
	response, replayed, e := s.bookings.ApplyStaffBookingCommandTx(ctx, tx, session.Staff.ID, id, input.Action, input.Reason, input.RequestKey, input.ExpectedUpdatedAt)
	switch {
	case errors.Is(e, appdata.ErrBookingChangeStale):
		return out, conflict
	case errors.Is(e, appdata.ErrBookingActionNotAllowed):
		return out, problem(409, "action_unavailable", "This action is no longer available. Refresh and review the booking’s status, timing, payment and agreement requirements.")
	case errors.Is(e, appdata.ErrBookingIdempotency):
		return out, problem(409, "idempotency_conflict", "This request key was used for a different command. Refresh before submitting a new action.")
	case errors.Is(e, appdata.ErrNotFound):
		return out, problem(404, "not_found", "Booking not found.")
	case e != nil:
		return out, e
	}
	e = tx.QueryRow(ctx, `SELECT id FROM booking_change_commands WHERE actor_type='staff' AND actor_id=$1 AND idempotency_key=$2`, session.Staff.ID, input.RequestKey).Scan(&out.CommandID)
	if e != nil {
		return out, e
	}
	out.Status = response.Status
	if !replayed {
		e = audit(ctx, tx, &session.Staff.ID, "booking."+input.Action, "booking", &id, input.Reason, map[string]any{"command_id": out.CommandID, "expected_updated_at": input.ExpectedUpdatedAt, "status": out.Status})
		if e != nil {
			return out, e
		}
	}
	return out, tx.Commit(ctx)
}

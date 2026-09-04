package appdata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"booking/go-server/internal/payments"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type reservationPaymentReconciler interface {
	ReconcileByPublicToken(context.Context, string) (payments.FinancialPayment, error)
}

// InboxAIReservationExpiryWorker releases capacity only after the canonical
// payment deadline has elapsed and every provider payment attempt is terminal.
// Provider reconciliation runs before the final booking transaction so an
// in-flight webhook and the expiry path serialize on the financial booking lock.
type InboxAIReservationExpiryWorker struct {
	db        *pgxpool.Pool
	repo      *Repository
	payments  reservationPaymentReconciler
	metrics   *InboxMetrics
	logger    *slog.Logger
	interval  time.Duration
	batchSize int
}

func NewInboxAIReservationExpiryWorker(
	db *pgxpool.Pool,
	repo *Repository,
	reconciler reservationPaymentReconciler,
	metrics *InboxMetrics,
	logger *slog.Logger,
) *InboxAIReservationExpiryWorker {
	if logger == nil {
		logger = slog.Default()
	}
	return &InboxAIReservationExpiryWorker{
		db: db, repo: repo, payments: reconciler, metrics: metrics, logger: logger,
		interval: 30 * time.Second, batchSize: 100,
	}
}

func (worker *InboxAIReservationExpiryWorker) Start(ctx context.Context) {
	if worker == nil || worker.db == nil || worker.repo == nil || worker.payments == nil {
		return
	}
	worker.runOnce(ctx)
	ticker := time.NewTicker(worker.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			worker.runOnce(ctx)
		}
	}
}

func (worker *InboxAIReservationExpiryWorker) runOnce(ctx context.Context) {
	bookingIDs, err := worker.listDueBookings(ctx)
	if err != nil {
		worker.metrics.ObserveReservationExpiry(false, false, err)
		worker.logger.Error("list due autopilot reservations failed", "error", err)
		return
	}
	for _, bookingID := range bookingIDs {
		expired, deferred, expireErr := worker.processBooking(ctx, bookingID)
		worker.metrics.ObserveReservationExpiry(expired, deferred, expireErr)
		if expireErr != nil && !errors.Is(expireErr, context.Canceled) {
			worker.logger.Warn("expire unpaid autopilot reservation failed", "booking_id", bookingID, "error", expireErr)
		}
	}
}

func (worker *InboxAIReservationExpiryWorker) listDueBookings(ctx context.Context) ([]uuid.UUID, error) {
	rows, err := worker.db.Query(ctx, `
		SELECT booking.id
		FROM bookings booking
		WHERE booking.reservation_expires_at<=NOW()
		  AND booking.reservation_expired_at IS NULL
		  AND booking.status NOT IN ('cancelled','canceled','declined','expired','completed','no_show')
		  AND booking.payment_status NOT IN ('deposit_paid_balance_due','paid_in_full')
		  AND EXISTS (
			SELECT 1 FROM inbox_ai_booking_sessions session WHERE session.booking_id=booking.id
		  )
		ORDER BY booking.reservation_expires_at, booking.id
		LIMIT $1
	`, worker.batchSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	bookingIDs := make([]uuid.UUID, 0, worker.batchSize)
	for rows.Next() {
		var bookingID uuid.UUID
		if err := rows.Scan(&bookingID); err != nil {
			return nil, err
		}
		bookingIDs = append(bookingIDs, bookingID)
	}
	return bookingIDs, rows.Err()
}

func (worker *InboxAIReservationExpiryWorker) processBooking(
	ctx context.Context,
	bookingID uuid.UUID,
) (bool, bool, error) {
	rows, err := worker.db.Query(ctx, `
		SELECT public_token FROM payments
		WHERE booking_id=$1 AND status IN ('created','pending','requires_action')
		ORDER BY created_at
	`, bookingID)
	if err != nil {
		return false, false, err
	}
	tokens := make([]string, 0, 2)
	for rows.Next() {
		var token string
		if err := rows.Scan(&token); err != nil {
			rows.Close()
			return false, false, err
		}
		tokens = append(tokens, token)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, false, err
	}
	for _, token := range tokens {
		if _, err := worker.payments.ReconcileByPublicToken(ctx, token); err != nil &&
			!errors.Is(err, payments.ErrConcurrentUpdate) &&
			!errors.Is(err, payments.ErrLedgerRecordNotFound) {
			// Provider uncertainty must preserve the reservation and its capacity.
			return false, true, err
		}
	}
	return worker.finalizeExpiry(ctx, bookingID)
}

func (worker *InboxAIReservationExpiryWorker) finalizeExpiry(
	ctx context.Context,
	bookingID uuid.UUID,
) (bool, bool, error) {
	tx, err := worker.db.Begin(ctx)
	if err != nil {
		return false, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var conversationID, clientID, marketplaceCustomerID, aiSessionID uuid.UUID
	if err := tx.QueryRow(ctx, `
		SELECT session.conversation_id, session.client_id,
			session.marketplace_customer_id, session.ai_session_id
		FROM inbox_ai_booking_sessions session
		WHERE session.booking_id=$1
	`, bookingID).Scan(
		&conversationID, &clientID, &marketplaceCustomerID, &aiSessionID,
	); errors.Is(err, pgx.ErrNoRows) {
		return false, false, nil
	} else if err != nil {
		return false, false, err
	}
	var conversationLock int
	if err := tx.QueryRow(ctx, `SELECT 1 FROM inbox_conversations WHERE id=$1 FOR UPDATE`, conversationID).Scan(
		&conversationLock,
	); err != nil {
		return false, false, err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, bookingID.String()); err != nil {
		return false, false, err
	}
	var status, paymentStatus string
	var deadline time.Time
	var expiredAt *time.Time
	if err := tx.QueryRow(ctx, `
		SELECT status, payment_status, reservation_expires_at, reservation_expired_at
		FROM bookings WHERE id=$1 FOR UPDATE
	`, bookingID).Scan(&status, &paymentStatus, &deadline, &expiredAt); errors.Is(err, pgx.ErrNoRows) {
		return false, false, nil
	} else if err != nil {
		return false, false, err
	}
	if expiredAt != nil || status == "completed" || isTerminalBookingStatus(status) ||
		initialBookingObligationSatisfied(paymentStatus) || time.Now().UTC().Before(deadline) {
		return false, false, tx.Commit(ctx)
	}
	var activeCount int
	var activeDeadline *time.Time
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*)::int, MAX(expires_at)
		FROM payments
		WHERE booking_id=$1 AND status IN ('created','pending','requires_action')
	`, bookingID).Scan(&activeCount, &activeDeadline); err != nil {
		return false, false, err
	}
	if activeCount > 0 {
		if activeDeadline != nil && activeDeadline.After(deadline) {
			if _, err := tx.Exec(ctx, `
				UPDATE bookings SET reservation_expires_at=$2, updated_at=NOW() WHERE id=$1
			`, bookingID, activeDeadline); err != nil {
				return false, false, err
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return false, false, err
		}
		return false, true, nil
	}
	now := time.Now().UTC()
	if _, err := tx.Exec(ctx, `
		UPDATE bookings SET status='expired', reservation_expired_at=$2,
			reservation_expiry_reason='payment_deadline_elapsed', updated_at=NOW()
		WHERE id=$1 AND reservation_expired_at IS NULL
	`, bookingID, now); err != nil {
		return false, false, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE agreement_instances SET status='expired', updated_at=NOW()
		WHERE booking_id=$1 AND status IN ('draft','awaiting_customer')
	`, bookingID); err != nil {
		return false, false, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE inbox_ai_booking_sessions SET state='expired', expires_at=$2,
			revision=revision+1, updated_at=NOW()
		WHERE booking_id=$1
	`, bookingID, now); err != nil {
		return false, false, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE inbox_ai_sessions SET state='expired', handoff_reason='reservation_payment_expired',
			expires_at=$2, completed_at=$2, revision=revision+1, updated_at=NOW()
		WHERE id=$1
	`, aiSessionID, now); err != nil {
		return false, false, err
	}
	presentationData, err := json.Marshal(map[string]any{
		"booking_id": bookingID.String(), "reason": "payment_deadline_elapsed",
		"label": "Reservation expired", "expired_at": now.Format(time.RFC3339),
		"href": "/booking/" + bookingID.String(),
	})
	if err != nil {
		return false, false, err
	}
	if _, _, err := worker.repo.appendInboxSystemWorkflowMessageTx(
		ctx, tx, clientID, marketplaceCustomerID, conversationID,
		uuid.NewSHA1(uuid.NameSpaceOID, []byte("inbox-reservation-expired:"+bookingID.String())),
		bookingID,
		"This reservation expired because payment was not completed before the deadline. The time is available again.",
		InboxMessagePresentation{Kind: "reservation_expired", Version: 1, Data: presentationData},
	); err != nil {
		return false, false, err
	}
	if err := appendInboxAISessionEvent(
		ctx, tx, conversationID, clientID, marketplaceCustomerID, "expired", "reservation_payment_expired",
	); err != nil {
		return false, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, false, fmt.Errorf("commit reservation expiry: %w", err)
	}
	return true, false, nil
}

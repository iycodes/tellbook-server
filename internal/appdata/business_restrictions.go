package appdata

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var ErrBusinessRestricted = errors.New("this business is not accepting new bookings")

func ensureBusinessAcceptsNewBookings(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	var restricted bool
	if e := tx.QueryRow(ctx, `SELECT platform_restricted FROM client_profiles WHERE client_id=$1 FOR SHARE`, id).Scan(&restricted); e != nil {
		return e
	}
	if restricted {
		return ErrBusinessRestricted
	}
	return nil
}

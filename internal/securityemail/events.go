// Package securityemail records account changes in the caller's transaction.
package securityemail

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type Account struct {
	Email       string
	HasPassword bool
}

func CaptureTx(ctx context.Context, tx pgx.Tx, realm string, id uuid.UUID) (Account, error) {
	table := "clients"
	if realm == "marketplace_customer" {
		table = "marketplace_customers"
	} else if realm != "provider" {
		return Account{}, fmt.Errorf("unsupported account realm")
	}
	var a Account
	err := tx.QueryRow(ctx, `SELECT CASE WHEN email_verified_at IS NOT NULL THEN COALESCE(email,'') ELSE '' END,password_hash IS NOT NULL FROM `+table+` WHERE id=$1 FOR UPDATE`, id).Scan(&a.Email, &a.HasPassword)
	return a, err
}

func RecordTx(ctx context.Context, tx pgx.Tx, realm string, id uuid.UUID, email, kind string, details map[string]string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return nil
	}
	if details == nil {
		details = map[string]string{}
	}
	payload, err := json.Marshal(details)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO account_security_events(id,realm,provider_client_id,marketplace_customer_id,recipient_email,kind,details)
 VALUES($1,$2,CASE WHEN $2='provider' THEN $3::uuid END,CASE WHEN $2='marketplace_customer' THEN $3::uuid END,$4,$5,$6)`, uuid.New(), realm, id, email, kind, payload)
	if err != nil {
		return fmt.Errorf("record account security event: %w", err)
	}
	return nil
}

// Local transaction settings enable the database triggers without depending on
// session state, including when the pool is behind PgBouncer.
func EnableTx(ctx context.Context, tx pgx.Tx, security, financial bool) error {
	_, err := tx.Exec(ctx, `SELECT set_config('tellbook.security_emails',$1,true),set_config('tellbook.financial_emails',$2,true)`, fmt.Sprint(security), fmt.Sprint(financial))
	return err
}

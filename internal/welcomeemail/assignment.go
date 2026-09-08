package welcomeemail

import (
	"context"
	"fmt"
	"html"
	"net/mail"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	AudienceProvider            = "provider"
	AudienceMarketplaceCustomer = "marketplace_customer"
)

type Assignment struct {
	Audience  string
	AccountID uuid.UUID
	Email     string
	Name      string
}

type template struct {
	ID      uuid.UUID
	Version int
	Subject string
	HTML    string
	Text    string
}

// AssignTx snapshots the active template and recipient into an idempotent job.
// It deliberately runs in the account-creation transaction so an account and
// its welcome assignment either both commit or neither does.
func AssignTx(ctx context.Context, tx pgx.Tx, assignment Assignment) error {
	_, err := AssignOptionalTx(ctx, tx, assignment)
	return err
}

// AssignOptionalTx snapshots the active template when one exists. A missing
// active template is an intentional no-op; validation and database failures
// remain visible to the account-creation transaction.
func AssignOptionalTx(ctx context.Context, tx pgx.Tx, assignment Assignment) (bool, error) {
	if tx == nil {
		return false, fmt.Errorf("welcome email transaction is required")
	}
	if assignment.Audience != AudienceProvider && assignment.Audience != AudienceMarketplaceCustomer {
		return false, fmt.Errorf("unsupported welcome email audience %q", assignment.Audience)
	}
	if assignment.AccountID == uuid.Nil {
		return false, fmt.Errorf("welcome email account id is required")
	}
	email, err := normalizeEmail(assignment.Email)
	if err != nil {
		return false, err
	}

	var selected template
	err = tx.QueryRow(ctx, `
		SELECT id,version,subject_template,html_template,text_template
		FROM welcome_email_templates
		WHERE audience=$1 AND status='active'
	`, assignment.Audience).Scan(
		&selected.ID, &selected.Version, &selected.Subject, &selected.HTML, &selected.Text,
	)
	if err == pgx.ErrNoRows {
		// An administrator may intentionally leave an audience without an active
		// template. Account creation must continue in that case.
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("load active welcome email template: %w", err)
	}

	name := strings.TrimSpace(assignment.Name)
	if name == "" {
		name = "there"
	}
	subject, err := renderTemplate(selected.Subject, name, email, false)
	if err != nil {
		return false, fmt.Errorf("render welcome email subject: %w", err)
	}
	if strings.ContainsAny(subject, "\r\n") {
		return false, fmt.Errorf("render welcome email subject: line breaks are not allowed")
	}
	htmlBody, err := renderTemplate(selected.HTML, name, email, true)
	if err != nil {
		return false, fmt.Errorf("render welcome email html: %w", err)
	}
	textBody, err := renderTemplate(selected.Text, name, email, false)
	if err != nil {
		return false, fmt.Errorf("render welcome email text: %w", err)
	}

	var providerID any
	var customerID any
	if assignment.Audience == AudienceProvider {
		providerID = assignment.AccountID
	} else {
		customerID = assignment.AccountID
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO welcome_email_jobs (
			idempotency_key,audience,provider_client_id,marketplace_customer_id,
			template_id,template_version,recipient_email,recipient_name,subject,html_body,text_body
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		ON CONFLICT (idempotency_key) DO NOTHING
	`, "welcome:"+assignment.Audience+":"+assignment.AccountID.String(), assignment.Audience,
		providerID, customerID, selected.ID, selected.Version, email, strings.TrimSpace(assignment.Name),
		subject, htmlBody, textBody)
	if err != nil {
		return false, fmt.Errorf("assign welcome email: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func normalizeEmail(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	parsed, err := mail.ParseAddress(value)
	if err != nil || parsed.Address != value {
		return "", fmt.Errorf("valid welcome email recipient is required")
	}
	return value, nil
}

func renderTemplate(source, name, email string, escapeHTML bool) (string, error) {
	if strings.Contains(source, "{{") {
		remaining := strings.NewReplacer("{{name}}", "", "{{email}}", "").Replace(source)
		if strings.Contains(remaining, "{{") {
			return "", fmt.Errorf("template contains an unsupported placeholder")
		}
	}
	if escapeHTML {
		name = html.EscapeString(name)
		email = html.EscapeString(email)
	}
	rendered := strings.NewReplacer("{{name}}", name, "{{email}}", email).Replace(source)
	if strings.TrimSpace(rendered) == "" {
		return "", fmt.Errorf("template rendered empty content")
	}
	return rendered, nil
}

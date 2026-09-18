package transactionemail

import (
	"errors"
	"strings"

	"booking/go-server/internal/mailer"
)

type SecurityKind string

const (
	PasswordChanged      SecurityKind = "password_changed"
	PasswordReset        SecurityKind = "password_reset"
	PasswordSet          SecurityKind = "password_set"
	EmailLinked          SecurityKind = "email_linked"
	PhoneLinked          SecurityKind = "phone_linked"
	PayoutAccountChanged SecurityKind = "payout_account_changed"
)

type SecurityInput struct {
	Event
	Kind SecurityKind
	// Identity and account numbers must be masked before they reach the email.
	PhoneLastFour   string
	InstitutionName string
	AccountLastFour string
}

func RenderSecurity(in SecurityInput) (mailer.Message, error) {
	v := view{Category: "ACCOUNT SECURITY", Preheader: "A security change was recorded for your TellBook account.",
		NoteTitle: "Don’t recognise this change?", NoteBody: "Open TellBook directly, review your account settings and secure your account. If you can’t access your account, use the password reset option on TellBook’s sign-in screen.",
		Footer:  "If you made this change, no further action is needed. TellBook will never ask you to reply with your password or verification code.",
		Signoff: "A little care for your account.", FooterLabel: "An account security notice from TellBook."}
	switch in.Kind {
	case PasswordChanged:
		v.Headline, v.Status = "Your password has changed.", "Password changed"
		v.Intro = "The password for your TellBook account was changed. This email is your record of the update."
	case PasswordReset:
		v.Headline, v.Status = "Your password reset is complete.", "Password reset completed"
		v.Intro = "A new password was saved through TellBook’s password reset flow."
	case PasswordSet:
		v.Headline, v.Status = "Your password is set.", "Password added"
		v.Intro = "A password was added to your TellBook account. You can now use it to sign in."
	case EmailLinked:
		v.Headline, v.Status = "An email has been linked.", "Email sign-in linked"
		v.Intro = "A verified email address was linked to your TellBook account for sign-in. Open TellBook directly to review your linked details."
	case PhoneLinked:
		if !lastFour.MatchString(in.PhoneLastFour) {
			return mailer.Message{}, errors.New("phone link email requires last four digits")
		}
		v.Headline, v.Status = "A phone number has been linked.", "Phone sign-in linked"
		v.Intro = "A verified phone number was linked to your TellBook account for sign-in."
		v.Rows = append(v.Rows, row{"PHONE NUMBER", "Ending " + in.PhoneLastFour})
	case PayoutAccountChanged, "payout_account_added", "payout_account_default_changed", "payout_account_removed":
		if !lastFour.MatchString(in.AccountLastFour) || strings.TrimSpace(in.InstitutionName) == "" {
			return mailer.Message{}, errors.New("payout account notice requires masked destination")
		}
		v.Headline, v.Status = "Your payout details changed.", "Payout account updated"
		v.Intro = "A change was recorded for the payout account below. Review your payout settings in TellBook to check your destinations and default account."
		switch in.Kind {
		case "payout_account_added":
			v.Headline, v.Status = "A payout account was added.", "Payout account added"
		case "payout_account_default_changed":
			v.Headline, v.Status = "Your default payout account changed.", "Default payout account changed"
		case "payout_account_removed":
			v.Headline, v.Status = "A payout account was removed.", "Payout account removed"
		}
		v.Rows = append(v.Rows, row{"PAYOUT ACCOUNT", in.InstitutionName + " · ending " + in.AccountLastFour})
		v.NoteBody = "Open TellBook directly and review your payout settings and account security. If you didn’t make this change, contact TellBook support through the app."
	default:
		return mailer.Message{}, errors.New("unsupported security email kind")
	}
	v.Subject = "TellBook security: " + v.Status
	v.Rows = append(v.Rows, in.recorded())
	return render(in.Event, "account-security", v)
}

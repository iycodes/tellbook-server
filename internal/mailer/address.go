package mailer

import (
	"net/mail"
	"regexp"
	"strings"
)

var mailboxPattern = regexp.MustCompile(`^[^\s<>,;:"\\]+@[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?)+$`)

// ValidMailbox accepts a single bare mailbox suitable for an SMTP envelope or
// Reply-To header. Display names, address lists and control characters are not allowed.
func ValidMailbox(value string) bool {
	if len(value) > 254 || !validHeaderValue(value) || !mailboxPattern.MatchString(value) {
		return false
	}
	address, err := mail.ParseAddress(value)
	if err != nil || address.Address != value {
		return false
	}
	local, _, _ := strings.Cut(value, "@")
	return !strings.HasPrefix(local, ".") && !strings.HasSuffix(local, ".") && !strings.Contains(local, "..")
}

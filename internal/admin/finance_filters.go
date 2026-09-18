package admin

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"slices"
	"strings"
	"time"
)

type financeQueryWindow struct {
	Start, End      time.Time
	From, To, Scope string
	After, AfterID  any
}

func financeWindow(kind string, f *FinanceFilter, statuses []string, now time.Time) (financeQueryWindow, error) {
	var w financeQueryWindow
	if f.From == "" {
		f.From = now.UTC().AddDate(0, 0, -6).Format("2006-01-02")
	}
	var e error
	w.Start, w.End, e = bookingWindow(BookingFilter{From: f.From, To: f.To}, now)
	if e != nil {
		return w, e
	}
	w.From, w.To = w.Start.Format("2006-01-02"), w.End.Format("2006-01-02")
	f.Q = strings.TrimSpace(f.Q)
	f.Currency = strings.ToUpper(strings.TrimSpace(f.Currency))
	if len([]rune(f.Q)) > 100 {
		return w, problem(422, "invalid_search", "Search up to 100 characters.")
	}
	if f.Currency != "" && !currencyPattern.MatchString(f.Currency) {
		return w, problem(422, "invalid_currency", "Use a three-letter currency code.")
	}
	if f.Status != "" && !slices.Contains(statuses, f.Status) {
		return w, problem(422, "invalid_status", "Choose a valid status for this record type.")
	}
	raw, _ := json.Marshal([]any{kind, w.From, w.To, f.Q, f.Status, f.Currency, f.BusinessID, f.BookingID})
	w.Scope = fmt.Sprintf("%x", sha256.Sum256(raw))
	if f.Cursor != "" {
		var c financeCursor
		decoded, e := base64.RawURLEncoding.DecodeString(f.Cursor)
		if e != nil || len(decoded) > 500 || json.Unmarshal(decoded, &c) != nil || c.Scope != w.Scope || c.ID == uuid.Nil || c.At.IsZero() {
			return w, problem(422, "invalid_cursor", "This page belongs to different filters. Start from the first page.")
		}
		w.After, w.AfterID = c.At, c.ID
	}
	return w, nil
}
func financeNextCursor(at time.Time, id uuid.UUID, scope string) string {
	raw, _ := json.Marshal(financeCursor{at, id, scope})
	return base64.RawURLEncoding.EncodeToString(raw)
}

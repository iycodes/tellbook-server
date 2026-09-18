package admin

import (
	"encoding/json"
	"errors"
	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
)

// Handler accepts only dedicated staff bearer sessions. Provider cookies/JWTs are
// never consulted. The Svelte server is responsible for its own browser cookie.
func (s *Service) Handler(logger *slog.Logger) http.Handler {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			next.ServeHTTP(w, r)
		})
	})
	type endpoint func(*http.Request, Session) (any, error)
	bind := func(method, path, cap string, fn endpoint) {
		r.MethodFunc(method, path, func(w http.ResponseWriter, r *http.Request) {
			var session Session
			var err error
			if cap != "public" {
				auth := r.Header.Get("Authorization")
				if !strings.HasPrefix(auth, "Bearer ") {
					err = unauthorized
				} else {
					session, err = s.Session(r.Context(), strings.TrimPrefix(auth, "Bearer "))
				}
				if err == nil && cap != "partial" && (session.Stage != "full" || session.Staff.Status != "active") {
					err = unauthorized
				}
				if err == nil && cap != "partial" && cap != "full" && !allowed(session.Staff.Role, cap) {
					err = forbidden
				}
			}
			var result any
			if err == nil {
				result, err = fn(r, session)
			}
			w.Header().Set("Content-Type", "application/json")
			if err != nil {
				var p *Problem
				if !errors.As(err, &p) {
					logger.Error("admin request failed", "error", err, "request_id", chimiddleware.GetReqID(r.Context()))
					p = &Problem{500, "unavailable", "The request could not be completed. Try again."}
				}
				w.WriteHeader(p.Status)
				json.NewEncoder(w).Encode(map[string]string{"code": p.Code, "message": p.Message, "request_id": chimiddleware.GetReqID(r.Context())})
				return
			}
			if result == nil {
				result = map[string]bool{"ok": true}
			}
			json.NewEncoder(w).Encode(result)
		})
	}
	bind("POST", "/auth/login", "public", func(r *http.Request, _ Session) (any, error) {
		var b struct {
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		if e := decode(r, &b); e != nil {
			return nil, e
		}
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		return s.Login(r.Context(), b.Email, b.Password, ip, r.UserAgent())
	})
	bind("GET", "/auth/session", "partial", func(_ *http.Request, p Session) (any, error) { return p, nil })
	bind("POST", "/auth/logout", "partial", func(r *http.Request, p Session) (any, error) { return nil, s.Logout(r.Context(), p) })
	bind("GET", "/auth/enrollment", "partial", func(r *http.Request, p Session) (any, error) { return s.Enrollment(r.Context(), p) })
	bind("POST", "/auth/mfa", "partial", func(r *http.Request, p Session) (any, error) {
		var b struct {
			Code string `json:"code"`
		}
		if e := decode(r, &b); e != nil {
			return nil, e
		}
		out, codes, e := s.MFA(r.Context(), p, b.Code, r.UserAgent())
		return map[string]any{"session": out, "recovery_codes": codes}, e
	})
	bind("POST", "/auth/invitation", "public", func(r *http.Request, _ Session) (any, error) {
		var b struct {
			Token string `json:"token"`
		}
		if e := decode(r, &b); e != nil {
			return nil, e
		}
		return s.Invitation(r.Context(), b.Token)
	})
	bind("POST", "/auth/accept-invitation", "public", func(r *http.Request, _ Session) (any, error) {
		var b struct {
			Token    string `json:"token"`
			Password string `json:"password"`
		}
		if e := decode(r, &b); e != nil {
			return nil, e
		}
		if e := s.limit(r.Context(), "accept:"+requestIP(r), 20); e != nil {
			return nil, e
		}
		return s.Accept(r.Context(), b.Token, b.Password, r.UserAgent())
	})
	bind("POST", "/auth/request-reset", "public", func(r *http.Request, _ Session) (any, error) {
		var b struct {
			Email string `json:"email"`
		}
		if e := decode(r, &b); e != nil {
			return nil, e
		}
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		return nil, s.RequestReset(r.Context(), b.Email, ip)
	})
	bind("POST", "/auth/reset-password", "public", func(r *http.Request, _ Session) (any, error) {
		var b struct {
			Token    string `json:"token"`
			Password string `json:"password"`
		}
		if e := decode(r, &b); e != nil {
			return nil, e
		}
		if e := s.limit(r.Context(), "reset-token:"+requestIP(r), 20); e != nil {
			return nil, e
		}
		return nil, s.ResetPassword(r.Context(), b.Token, b.Password)
	})
	bind("GET", "/reports/overview", "reports.read", func(r *http.Request, p Session) (any, error) {
		return s.Overview(r.Context(), p, r.URL.Query().Get("from"), r.URL.Query().Get("to"))
	})
	bind("GET", "/staff/assignees", "support.manage", func(r *http.Request, _ Session) (any, error) { return s.SupportAssignees(r.Context()) })
	bind("GET", "/support/cases", "support.read", func(r *http.Request, p Session) (any, error) {
		q := r.URL.Query()
		return s.SupportCases(r.Context(), p.Staff.ID, SupportFilter{Q: q.Get("q"), Status: q.Get("status"), Priority: q.Get("priority"), Owner: q.Get("owner"), Due: q.Get("due"), Cursor: q.Get("cursor")})
	})
	bind("POST", "/support/cases", "support.manage", func(r *http.Request, p Session) (any, error) {
		var b SupportInput
		if e := decode(r, &b); e != nil {
			return nil, e
		}
		return s.SaveSupportCase(r.Context(), p, uuid.Nil, b)
	})
	bind("GET", "/support/cases/{id}", "support.read", func(r *http.Request, _ Session) (any, error) {
		id, e := pathID(r)
		if e != nil {
			return nil, e
		}
		return s.SupportCase(r.Context(), id)
	})
	bind("POST", "/support/cases/{id}", "support.manage", func(r *http.Request, p Session) (any, error) {
		id, e := pathID(r)
		if e != nil {
			return nil, e
		}
		var b SupportInput
		if e = decode(r, &b); e != nil {
			return nil, e
		}
		return s.SaveSupportCase(r.Context(), p, id, b)
	})
	bind("GET", "/staff", "staff.manage", func(r *http.Request, _ Session) (any, error) { return s.StaffList(r.Context()) })
	bind("POST", "/staff", "staff.manage", func(r *http.Request, p Session) (any, error) {
		var b struct {
			Email string `json:"email"`
			Name  string `json:"name"`
			Role  string `json:"role"`
		}
		if e := decode(r, &b); e != nil {
			return nil, e
		}
		state, e := s.Invite(r.Context(), p, b.Email, b.Name, b.Role)
		return map[string]string{"delivery_state": state}, e
	})
	bind("POST", "/staff/{id}", "staff.manage", func(r *http.Request, p Session) (any, error) {
		id, e := pathID(r)
		if e != nil {
			return nil, e
		}
		var b StaffChange
		if e = decode(r, &b); e != nil {
			return nil, e
		}
		state, e := s.ChangeStaff(r.Context(), p, id, b)
		return map[string]string{"delivery_state": state}, e
	})
	bind("GET", "/security/sessions", "full", func(r *http.Request, p Session) (any, error) { return s.Security(r.Context(), p) })
	bind("POST", "/security/sessions/{id}/revoke", "full", func(r *http.Request, p Session) (any, error) {
		id, e := pathID(r)
		if e != nil {
			return nil, e
		}
		return nil, s.RevokeSession(r.Context(), p, id)
	})
	bind("POST", "/security/recovery-codes", "full", func(r *http.Request, p Session) (any, error) {
		if e := s.limit(r.Context(), "rotate:"+p.Staff.ID.String(), 5); e != nil {
			return nil, e
		}
		var b struct {
			Password string `json:"password"`
			Code     string `json:"code"`
		}
		if e := decode(r, &b); e != nil {
			return nil, e
		}
		codes, e := s.RotateRecovery(r.Context(), p, b.Password, b.Code)
		return map[string]any{"recovery_codes": codes}, e
	})

	bind("POST", "/finance/requests/preview", "finance.manage", func(r *http.Request, p Session) (any, error) {
		var b PayoutReviewInput
		if e := decode(r, &b); e != nil {
			return nil, e
		}
		return s.PreviewFinancialRequest(r.Context(), p, b)
	})
	bind("POST", "/finance/requests", "finance.manage", func(r *http.Request, p Session) (any, error) {
		var b FinancialRequestInput
		if e := decode(r, &b); e != nil {
			return nil, e
		}
		return s.CreateFinancialRequest(r.Context(), p, b)
	})
	bind("GET", "/finance/requests", "finance.read", func(r *http.Request, _ Session) (any, error) {
		business, e := queryID(r, "business")
		if e != nil {
			return nil, e
		}
		booking, e := queryID(r, "booking")
		if e != nil {
			return nil, e
		}
		q := r.URL.Query()
		return s.FinancialRequests(r.Context(), FinanceFilter{From: q.Get("from"), To: q.Get("to"), Q: q.Get("q"), Status: q.Get("status"), Currency: q.Get("currency"), Cursor: q.Get("cursor"), BusinessID: business, BookingID: booking})
	})
	bind("GET", "/finance/requests/{id}", "finance.read", func(r *http.Request, _ Session) (any, error) {
		id, e := pathID(r)
		if e != nil {
			return nil, e
		}
		return s.FinancialRequest(r.Context(), id)
	})
	bind("POST", "/finance/requests/{id}/decision", "finance.manage", func(r *http.Request, p Session) (any, error) {
		id, e := pathID(r)
		if e != nil {
			return nil, e
		}
		var b FinancialDecision
		if e = decode(r, &b); e != nil {
			return nil, e
		}
		return s.DecideFinancialRequest(r.Context(), p, id, b)
	})
	for _, kind := range []string{"payouts", "refunds"} {
		bind("GET", "/finance/"+kind, "finance.read", func(r *http.Request, _ Session) (any, error) {
			business, e := queryID(r, "business")
			if e != nil {
				return nil, e
			}
			booking, e := queryID(r, "booking")
			if e != nil {
				return nil, e
			}
			q := r.URL.Query()
			return s.FinanceTransfers(r.Context(), kind, FinanceFilter{From: q.Get("from"), To: q.Get("to"), Q: q.Get("q"), Status: q.Get("status"), Currency: q.Get("currency"), Cursor: q.Get("cursor"), BusinessID: business, BookingID: booking})
		})
		bind("GET", "/finance/"+kind+"/{id}", "finance.read", func(r *http.Request, _ Session) (any, error) {
			id, e := pathID(r)
			if e != nil {
				return nil, e
			}
			if kind == "payouts" {
				return s.FinancePayoutDetail(r.Context(), id)
			}
			return s.FinanceRefundDetail(r.Context(), id)
		})
	}
	bind("GET", "/finance/payments", "finance.read", func(r *http.Request, _ Session) (any, error) {
		business, e := queryID(r, "business")
		if e != nil {
			return nil, e
		}
		booking, e := queryID(r, "booking")
		if e != nil {
			return nil, e
		}
		q := r.URL.Query()
		return s.FinancePayments(r.Context(), FinanceFilter{From: q.Get("from"), To: q.Get("to"), Q: q.Get("q"), Status: q.Get("status"), Currency: q.Get("currency"), Cursor: q.Get("cursor"), BusinessID: business, BookingID: booking})
	})
	bind("GET", "/finance/payments/{id}", "finance.read", func(r *http.Request, _ Session) (any, error) {
		id, e := pathID(r)
		if e != nil {
			return nil, e
		}
		return s.FinancePaymentDetail(r.Context(), id)
	})
	bind("GET", "/bookings", "bookings.read", func(r *http.Request, _ Session) (any, error) {
		business, e := queryID(r, "business")
		if e != nil {
			return nil, e
		}
		contact, e := queryID(r, "contact")
		if e != nil {
			return nil, e
		}
		account, e := queryID(r, "account")
		if e != nil {
			return nil, e
		}
		q := r.URL.Query()
		return s.Bookings(r.Context(), BookingFilter{From: q.Get("from"), To: q.Get("to"), Q: q.Get("q"), Status: q.Get("status"), Cursor: q.Get("cursor"), BusinessID: business, ContactID: contact, AccountID: account})
	})
	bind("GET", "/bookings/{id}", "bookings.read", func(r *http.Request, p Session) (any, error) {
		id, e := pathID(r)
		if e != nil {
			return nil, e
		}
		detail, e := s.BookingDetail(r.Context(), id)
		if !allowed(p.Staff.Role, "bookings.manage") {
			detail.AllowedActions = []string{}
		}
		return detail, e
	})
	bind("POST", "/bookings/{id}/commands", "bookings.manage", func(r *http.Request, p Session) (any, error) {
		id, e := pathID(r)
		if e != nil {
			return nil, e
		}
		var command BookingCommand
		if e = decode(r, &command); e != nil {
			return nil, e
		}
		return s.CommandBooking(r.Context(), p, id, command)
	})
	bind("GET", "/customers/{kind}", "customers.read", func(r *http.Request, _ Session) (any, error) {
		business, e := queryID(r, "business")
		if e != nil {
			return nil, e
		}
		q := r.URL.Query()
		return s.Customers(r.Context(), CustomerFilter{Kind: chi.URLParam(r, "kind"), Q: q.Get("q"), Cursor: q.Get("cursor"), BusinessID: business})
	})
	bind("GET", "/customers/{kind}/{id}", "customers.read", func(r *http.Request, _ Session) (any, error) {
		id, e := pathID(r)
		if e != nil {
			return nil, e
		}
		return s.CustomerRecord(r.Context(), chi.URLParam(r, "kind"), id)
	})
	bind("GET", "/businesses", "businesses.read", func(r *http.Request, _ Session) (any, error) {
		cursor, e := queryID(r, "cursor")
		if e != nil {
			return nil, e
		}
		return s.Businesses(r.Context(), r.URL.Query().Get("q"), r.URL.Query().Get("verification"), cursor)
	})
	bind("GET", "/businesses/{id}", "businesses.read", func(r *http.Request, _ Session) (any, error) {
		id, e := pathID(r)
		if e != nil {
			return nil, e
		}
		return s.BusinessDetail(r.Context(), id)
	})
	bind("POST", "/businesses/{id}/decisions", "businesses.manage", func(r *http.Request, p Session) (any, error) {
		id, e := pathID(r)
		if e != nil {
			return nil, e
		}
		var b BusinessDecision
		if e = decode(r, &b); e != nil {
			return nil, e
		}
		return nil, s.DecideBusiness(r.Context(), p, id, b)
	})
	for _, target := range []struct{ path, kind, read, write string }{
		{"/support/cases/{id}/notes", "support_case", "support.read", "support.manage"},
		{"/businesses/{id}/notes", "business", "businesses.read", "businesses.note"},
		{"/bookings/{id}/notes", "booking", "bookings.read", "bookings.note"},
		{"/customers/contacts/{id}/notes", "customer_contact", "customers.read", "customers.note"},
		{"/customers/accounts/{id}/notes", "marketplace_account", "customers.read", "customers.note"},
	} {
		bind("GET", target.path, target.read, func(r *http.Request, _ Session) (any, error) {
			id, e := pathID(r)
			if e != nil {
				return nil, e
			}
			cursor, e := queryID(r, "cursor")
			if e != nil {
				return nil, e
			}
			return s.RecordNotes(r.Context(), target.kind, id, cursor)
		})
		bind("POST", target.path, target.write, func(r *http.Request, p Session) (any, error) {
			id, e := pathID(r)
			if e != nil {
				return nil, e
			}
			var b struct {
				Body       string    `json:"body"`
				RequestKey uuid.UUID `json:"request_key"`
			}
			if e = decode(r, &b); e != nil {
				return nil, e
			}
			return s.AddRecordNote(r.Context(), p, target.kind, id, b.RequestKey, b.Body)
		})
	}
	bind("GET", "/businesses/{id}/activity", "businesses.read", func(r *http.Request, _ Session) (any, error) {
		id, e := pathID(r)
		if e != nil {
			return nil, e
		}
		cursor, e := queryID(r, "cursor")
		if e != nil {
			return nil, e
		}
		return s.Audit(r.Context(), &id, cursor)
	})
	bind("GET", "/audit", "audit.read", func(r *http.Request, _ Session) (any, error) {
		cursor, e := queryID(r, "cursor")
		if e != nil {
			return nil, e
		}
		return s.Audit(r.Context(), nil, cursor)
	})
	return r
}
func decode(r *http.Request, out any) error {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return problem(415, "content_type", "Use application/json.")
	}
	d := json.NewDecoder(io.LimitReader(r.Body, 20000))
	d.DisallowUnknownFields()
	if e := d.Decode(out); e != nil {
		return problem(422, "invalid_input", "Check the submitted fields.")
	}
	if d.Decode(new(any)) != io.EOF {
		return problem(422, "invalid_input", "Submit one JSON object.")
	}
	return nil
}
func pathID(r *http.Request) (uuid.UUID, error) {
	id, e := uuid.Parse(chi.URLParam(r, "id"))
	if e != nil || id == uuid.Nil {
		return uuid.Nil, problem(422, "invalid_id", "Invalid record ID.")
	}
	return id, nil
}
func queryID(r *http.Request, key string) (*uuid.UUID, error) {
	v := r.URL.Query().Get(key)
	if v == "" {
		return nil, nil
	}
	id, e := uuid.Parse(v)
	if e != nil || id == uuid.Nil {
		return nil, problem(422, "invalid_cursor", "Invalid page cursor.")
	}
	return &id, nil
}

func requestIP(r *http.Request) string {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}

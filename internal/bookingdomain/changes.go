package bookingdomain

import (
	"strings"
	"time"
)

const BasisPointsFull = int64(10000)

type ChangePolicy struct {
	CancellationNoticeMinutes int
	CancellationRefundBPS     int64
	RescheduleNoticeMinutes   int
	RescheduleFeeMinor        int64
	AutomatedReschedule       bool
}

type BookingActionPermissions struct {
	CustomerCancel     bool
	CustomerReschedule bool
	ProviderConfirm    bool
	ProviderDecline    bool
	ProviderComplete   bool
	ProviderNoShow     bool
}

func StructuredPolicy(policyText string) ChangePolicy {
	normalized := strings.ToLower(strings.TrimSpace(policyText))
	switch {
	case normalized == "no cancellation fee":
		return ChangePolicy{CancellationRefundBPS: BasisPointsFull, AutomatedReschedule: true}
	case strings.Contains(normalized, "48h") || strings.Contains(normalized, "48 hour"):
		return ChangePolicy{
			CancellationNoticeMinutes: 48 * 60,
			CancellationRefundBPS:     BasisPointsFull,
			RescheduleNoticeMinutes:   48 * 60,
			AutomatedReschedule:       true,
		}
	case strings.Contains(normalized, "24h") || strings.Contains(normalized, "24 hour"):
		return ChangePolicy{
			CancellationNoticeMinutes: 24 * 60,
			CancellationRefundBPS:     BasisPointsFull,
			RescheduleNoticeMinutes:   24 * 60,
			AutomatedReschedule:       true,
		}
	case strings.Contains(normalized, "no refund") || strings.Contains(normalized, "non-refundable"):
		return ChangePolicy{
			RescheduleNoticeMinutes: 24 * 60,
			AutomatedReschedule:     true,
		}
	default:
		// Unknown free-text policies must never cause an automatic refund or
		// reschedule whose financial consequence Tellbook cannot prove.
		return ChangePolicy{}
	}
}

func Permissions(status string, startsAt, endsAt, now time.Time, policy ChangePolicy) BookingActionPermissions {
	status = strings.ToLower(strings.TrimSpace(status))
	active := status == "booked" || status == "confirmed"
	rescheduleDeadline := startsAt.Add(-time.Duration(policy.RescheduleNoticeMinutes) * time.Minute)
	return BookingActionPermissions{
		CustomerCancel:     active && now.Before(startsAt),
		CustomerReschedule: active && policy.AutomatedReschedule && now.Before(startsAt) && !now.After(rescheduleDeadline),
		ProviderConfirm:    status == "booked" && now.Before(endsAt),
		ProviderDecline:    status == "booked" && now.Before(startsAt),
		ProviderComplete:   status == "confirmed" && !now.Before(endsAt),
		ProviderNoShow:     status == "confirmed" && !now.Before(endsAt),
	}
}

func CancellationRefund(netPaidMinor int64, startsAt, now time.Time, policy ChangePolicy) int64 {
	if netPaidMinor <= 0 || policy.CancellationRefundBPS <= 0 {
		return 0
	}
	deadline := startsAt.Add(-time.Duration(policy.CancellationNoticeMinutes) * time.Minute)
	if now.After(deadline) {
		return 0
	}
	return netPaidMinor * policy.CancellationRefundBPS / BasisPointsFull
}

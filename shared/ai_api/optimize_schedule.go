package aiapi

type OptimizeScheduleRequest struct {
	Timezone             string               `json:"timezone"`
	WeeklyAvailability   []ScheduleDay        `json:"weekly_availability"`
	BlockedPeriods       []BlockedPeriod      `json:"blocked_periods,omitempty"`
	Services             []ScheduleService    `json:"services,omitempty"`
	BookingRules         ScheduleBookingRules `json:"booking_rules"`
	AnalysisWindowStart  string               `json:"analysis_window_start,omitempty"`
	AnalysisWindowEnd    string               `json:"analysis_window_end,omitempty"`
	BookingsInWindow     []ScheduledBooking   `json:"bookings_in_window,omitempty"`
	OptimizationGoal     string               `json:"optimization_goal,omitempty"`
	AllowedAdjustments   []string             `json:"allowed_adjustments,omitempty"`
	ProtectedConstraints []string             `json:"protected_constraints,omitempty"`
}

type ScheduleDay struct {
	Day     string       `json:"day"`
	IsOpen  bool         `json:"is_open"`
	Windows []TimeWindow `json:"windows,omitempty"`
}

type TimeWindow struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

type BlockedPeriod struct {
	StartAt string `json:"start_at"`
	EndAt   string `json:"end_at"`
	Reason  string `json:"reason,omitempty"`
}

type ScheduleService struct {
	ServiceID         string `json:"service_id,omitempty"`
	Title             string `json:"title"`
	DurationMinutes   int    `json:"duration_minutes"`
	PrepTimeMinutes   int    `json:"prep_time_minutes,omitempty"`
	BufferTimeMinutes int    `json:"buffer_time_minutes,omitempty"`
	LeadTime          string `json:"lead_time,omitempty"`
	LastBooking       string `json:"last_booking,omitempty"`
	MaxPerDay         int    `json:"max_per_day,omitempty"`
}

type ScheduleBookingRules struct {
	DefaultLeadTime      string `json:"default_lead_time,omitempty"`
	DefaultLastBooking   string `json:"default_last_booking,omitempty"`
	MaxBookingsPerDay    int    `json:"max_bookings_per_day,omitempty"`
	AllowBackToBack      bool   `json:"allow_back_to_back"`
	GapPreferenceMinutes int    `json:"gap_preference_minutes,omitempty"`
}

type ScheduledBooking struct {
	ServiceID string `json:"service_id,omitempty"`
	StartAt   string `json:"start_at"`
	EndAt     string `json:"end_at"`
	Status    string `json:"status,omitempty"`
}

type RecentBooking struct {
	ServiceID    string `json:"service_id,omitempty"`
	StartAt      string `json:"start_at"`
	EndAt        string `json:"end_at"`
	Status       string `json:"status,omitempty"`
	CustomerType string `json:"customer_type,omitempty"`
}

//

type ScheduleOptimizationSuggestion struct {
	Area           string  `json:"area"`
	Issue          string  `json:"issue"`
	Recommendation string  `json:"recommendation"`
	Reason         string  `json:"reason,omitempty"`
	Impact         string  `json:"impact,omitempty"`
	Confidence     float64 `json:"confidence"`
}

type ScheduleOptimizationSummary struct {
	UtilizationNote string `json:"utilization_note,omitempty"`
	GapNote         string `json:"gap_note,omitempty"`
	OverloadNote    string `json:"overload_note,omitempty"`
}

type ScheduleOptimizationDigest struct {
	Headline     string   `json:"headline"`
	ShortSummary string   `json:"short_summary"`
	TopActions   []string `json:"top_actions,omitempty"`
	CallToAction string   `json:"call_to_action,omitempty"`
}

type OptimizeScheduleResponse struct {
	OptimizationsAvailable bool                             `json:"optimizations_available"`
	Score                  int                              `json:"score,omitempty"`
	Summary                string                           `json:"summary,omitempty"`
	OptimizationSummary    ScheduleOptimizationSummary      `json:"optimization_summary,omitempty"`
	Suggestions            []ScheduleOptimizationSuggestion `json:"suggestions,omitempty"`
	Warnings               []Warning                        `json:"warnings,omitempty"`
	Digest                 *ScheduleOptimizationDigest      `json:"digest,omitempty"`
}

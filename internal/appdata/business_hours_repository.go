package appdata

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrBusinessHoursRequired = errors.New("at least one open day is required")
	ErrInvalidBusinessHours  = errors.New("business hours are invalid")
	ErrDuplicateBusinessDay  = errors.New("business hours contain a duplicate day")
)

func (r *Repository) GetBusinessHours(ctx context.Context, clientID uuid.UUID) (BusinessHoursResponse, error) {
	rows, err := r.db.Query(ctx, `
		SELECT
			day_of_week,
			TO_CHAR(start_time, 'HH24:MI'),
			TO_CHAR(end_time, 'HH24:MI')
		FROM provider_availability_windows
		WHERE client_id = $1
		ORDER BY day_of_week ASC, start_time ASC
	`, clientID)
	if err != nil {
		return BusinessHoursResponse{}, fmt.Errorf("list business hours: %w", err)
	}
	defer rows.Close()

	items := make([]BusinessHoursWindow, 0)
	for rows.Next() {
		var item BusinessHoursWindow
		if err := rows.Scan(&item.DayOfWeek, &item.StartTime, &item.EndTime); err != nil {
			return BusinessHoursResponse{}, fmt.Errorf("scan business hours: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return BusinessHoursResponse{}, fmt.Errorf("iterate business hours: %w", err)
	}

	return BusinessHoursResponse{Items: items}, nil
}

func (r *Repository) UpdateBusinessHours(ctx context.Context, clientID uuid.UUID, input UpdateBusinessHoursInput) (BusinessHoursResponse, error) {
	items, err := normalizeBusinessHours(input.Items)
	if err != nil {
		return BusinessHoursResponse{}, err
	}

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return BusinessHoursResponse{}, fmt.Errorf("begin business hours update: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `DELETE FROM provider_availability_windows WHERE client_id = $1`, clientID); err != nil {
		return BusinessHoursResponse{}, fmt.Errorf("clear business hours: %w", err)
	}

	for _, item := range items {
		if _, err := tx.Exec(ctx, `
			INSERT INTO provider_availability_windows (
				id, client_id, day_of_week, start_time, end_time, slot_interval_minutes
			)
			VALUES ($1, $2, $3, $4::time, $5::time, 30)
		`, uuid.New(), clientID, item.DayOfWeek, item.StartTime, item.EndTime); err != nil {
			return BusinessHoursResponse{}, fmt.Errorf("insert business hours: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return BusinessHoursResponse{}, fmt.Errorf("commit business hours update: %w", err)
	}

	return BusinessHoursResponse{Items: items}, nil
}

func normalizeBusinessHours(items []BusinessHoursWindow) ([]BusinessHoursWindow, error) {
	if len(items) == 0 {
		return nil, ErrBusinessHoursRequired
	}
	if len(items) > 7 {
		return nil, ErrInvalidBusinessHours
	}

	normalized := make([]BusinessHoursWindow, 0, len(items))
	seenDays := make(map[int]struct{}, len(items))
	for _, item := range items {
		if item.DayOfWeek < 0 || item.DayOfWeek > 6 {
			return nil, ErrInvalidBusinessHours
		}
		if _, exists := seenDays[item.DayOfWeek]; exists {
			return nil, ErrDuplicateBusinessDay
		}

		startTime, err := time.Parse("15:04", strings.TrimSpace(item.StartTime))
		if err != nil {
			return nil, ErrInvalidBusinessHours
		}
		endTime, err := time.Parse("15:04", strings.TrimSpace(item.EndTime))
		if err != nil || !endTime.After(startTime) {
			return nil, ErrInvalidBusinessHours
		}

		seenDays[item.DayOfWeek] = struct{}{}
		normalized = append(normalized, BusinessHoursWindow{
			DayOfWeek: item.DayOfWeek,
			StartTime: startTime.Format("15:04"),
			EndTime:   endTime.Format("15:04"),
		})
	}

	return normalized, nil
}

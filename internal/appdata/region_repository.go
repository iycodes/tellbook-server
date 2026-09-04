package appdata

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type regionAssignment struct {
	CountryCode   string
	StateRegionID *uuid.UUID
	StateName     string
	LGARegionID   *uuid.UUID
	LGAName       string
	Locality      string
}

func (r *Repository) assignAdministrativeRegions(ctx context.Context, latitude, longitude float64, locality string) (regionAssignment, error) {
	assignment := regionAssignment{Locality: strings.TrimSpace(locality)}
	var stateID, lgaID uuid.UUID
	err := r.db.QueryRow(ctx, `
		SELECT
			state.id,
			state.name,
			lga.id,
			lga.name,
			state.country_code
		FROM administrative_regions lga
		INNER JOIN administrative_regions state ON state.id = lga.parent_id
		WHERE lga.level = 'lga'
		  AND ST_Covers(lga.boundary, ST_SetSRID(ST_MakePoint($1, $2), 4326))
		ORDER BY ST_Area(lga.boundary::geography) ASC
		LIMIT 1
	`, longitude, latitude).Scan(
		&stateID, &assignment.StateName, &lgaID, &assignment.LGAName, &assignment.CountryCode,
	)
	if err == nil {
		assignment.StateRegionID = &stateID
		assignment.LGARegionID = &lgaID
		return assignment, nil
	}
	if err != nil && err != pgx.ErrNoRows {
		return regionAssignment{}, fmt.Errorf("assign administrative region: %w", err)
	}

	err = r.db.QueryRow(ctx, `
		SELECT id, name, country_code
		FROM administrative_regions
		WHERE level = 'state'
		  AND ST_Covers(boundary, ST_SetSRID(ST_MakePoint($1, $2), 4326))
		ORDER BY ST_Area(boundary::geography) ASC
		LIMIT 1
	`, longitude, latitude).Scan(&stateID, &assignment.StateName, &assignment.CountryCode)
	if err == pgx.ErrNoRows {
		return assignment, nil
	}
	if err != nil {
		return regionAssignment{}, fmt.Errorf("assign state region: %w", err)
	}
	assignment.StateRegionID = &stateID
	return assignment, nil
}

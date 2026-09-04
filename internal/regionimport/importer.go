package regionimport

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	countryCode   = "NG"
	sourceName    = "GRID3 Nigeria via geoBoundaries gbOpen"
	sourceVersion = "boundary year 2022; geoBoundaries commit 9469f09"
	sourceLicense = "CC BY 4.0"
)

var nonSlugCharacters = regexp.MustCompile(`[^a-z0-9]+`)

type featureCollection struct {
	Type     string    `json:"type"`
	Features []feature `json:"features"`
}

type feature struct {
	Type       string          `json:"type"`
	Properties featureProperty `json:"properties"`
	Geometry   json.RawMessage `json:"geometry"`
}

type featureProperty struct {
	ShapeGroup string `json:"shapeGroup"`
	ShapeID    string `json:"shapeID"`
	ShapeISO   string `json:"shapeISO"`
	ShapeName  string `json:"shapeName"`
	ShapeType  string `json:"shapeType"`
}

type Result struct {
	States               int
	LGAs                 int
	BackfilledBusinesses int64
	BackfilledResolved   int64
}

type Importer struct {
	db *pgxpool.Pool
}

func New(db *pgxpool.Pool) *Importer {
	return &Importer{db: db}
}

func (i *Importer) Import(ctx context.Context, adm1Reader, adm2Reader io.Reader) (Result, error) {
	adm1, err := decodeCollection(adm1Reader, "ADM1", 37)
	if err != nil {
		return Result{}, err
	}
	adm2, err := decodeCollection(adm2Reader, "ADM2", 774)
	if err != nil {
		return Result{}, err
	}

	tx, err := i.db.Begin(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("begin region import: %w", err)
	}
	defer tx.Rollback(ctx)

	countryID := deterministicID(countryCode)
	if _, err := tx.Exec(ctx, `
		INSERT INTO administrative_regions (
			id, parent_id, country_code, level, code, slug, name, boundary,
			source, source_version, source_feature_id, source_license, is_active,
			created_at, updated_at
		) VALUES ($1, NULL, 'NG', 'country', 'NG', 'nigeria', 'Nigeria', NULL,
			$2, $3, '', $4, TRUE, NOW(), NOW())
		ON CONFLICT (country_code, code) DO UPDATE SET
			name = EXCLUDED.name,
			source = EXCLUDED.source,
			source_version = EXCLUDED.source_version,
			source_license = EXCLUDED.source_license,
			is_active = TRUE,
			updated_at = NOW()
	`, countryID, sourceName, sourceVersion, sourceLicense); err != nil {
		return Result{}, fmt.Errorf("upsert Nigeria region: %w", err)
	}

	for _, item := range adm1.Features {
		code := strings.ToUpper(strings.TrimSpace(item.Properties.ShapeISO))
		name := strings.TrimSpace(item.Properties.ShapeName)
		if code == "" || name == "" {
			return Result{}, fmt.Errorf("ADM1 feature %q has no canonical code or name", item.Properties.ShapeID)
		}
		if err := upsertPolygonRegion(ctx, tx, polygonRegionInput{
			ID:              deterministicID(code),
			ParentID:        countryID,
			Level:           "state",
			Code:            code,
			Slug:            slugify(name),
			Name:            name,
			SourceFeatureID: strings.TrimSpace(item.Properties.ShapeID),
			Geometry:        item.Geometry,
		}); err != nil {
			return Result{}, fmt.Errorf("import state %q: %w", name, err)
		}
	}

	for _, item := range adm2.Features {
		name := strings.TrimSpace(item.Properties.ShapeName)
		if name == "" {
			return Result{}, fmt.Errorf("ADM2 feature %q has no name", item.Properties.ShapeID)
		}
		parentID, parentCode, err := locateParentState(ctx, tx, item.Geometry)
		if err != nil {
			return Result{}, fmt.Errorf("assign LGA %q to state: %w", name, err)
		}
		code := parentCode + "-" + strings.ToUpper(slugify(name))
		if err := upsertPolygonRegion(ctx, tx, polygonRegionInput{
			ID:              deterministicID(code),
			ParentID:        parentID,
			Level:           "lga",
			Code:            code,
			Slug:            slugify(name),
			Name:            name,
			SourceFeatureID: strings.TrimSpace(item.Properties.ShapeID),
			Geometry:        item.Geometry,
		}); err != nil {
			return Result{}, fmt.Errorf("import LGA %q: %w", name, err)
		}
	}

	result := Result{}
	if err := tx.QueryRow(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE level = 'state' AND country_code = 'NG' AND is_active),
			COUNT(*) FILTER (WHERE level = 'lga' AND country_code = 'NG' AND is_active)
		FROM administrative_regions
	`).Scan(&result.States, &result.LGAs); err != nil {
		return Result{}, fmt.Errorf("validate imported region counts: %w", err)
	}
	if result.States != 37 || result.LGAs != 774 {
		return Result{}, fmt.Errorf("unexpected imported region counts: states=%d LGAs=%d", result.States, result.LGAs)
	}

	var invalidGeometryCount int
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM administrative_regions
		WHERE country_code = 'NG' AND level IN ('state', 'lga')
		  AND (boundary IS NULL OR ST_IsEmpty(boundary) OR NOT ST_IsValid(boundary))
	`).Scan(&invalidGeometryCount); err != nil {
		return Result{}, fmt.Errorf("count invalid imported geometry: %w", err)
	}
	if invalidGeometryCount != 0 {
		return Result{}, fmt.Errorf("region import left %d invalid geometries", invalidGeometryCount)
	}

	businessTag, err := tx.Exec(ctx, regionBackfillSQL("business_locations", "updated_at = NOW(),"))
	if err != nil {
		return Result{}, fmt.Errorf("backfill business location regions: %w", err)
	}
	result.BackfilledBusinesses = businessTag.RowsAffected()

	resolvedTag, err := tx.Exec(ctx, regionBackfillSQL("resolved_locations", ""))
	if err != nil {
		return Result{}, fmt.Errorf("backfill resolved location regions: %w", err)
	}
	result.BackfilledResolved = resolvedTag.RowsAffected()

	if err := tx.Commit(ctx); err != nil {
		return Result{}, fmt.Errorf("commit region import: %w", err)
	}
	return result, nil
}

type polygonRegionInput struct {
	ID              uuid.UUID
	ParentID        uuid.UUID
	Level           string
	Code            string
	Slug            string
	Name            string
	SourceFeatureID string
	Geometry        json.RawMessage
}

func upsertPolygonRegion(ctx context.Context, tx pgx.Tx, input polygonRegionInput) error {
	commandTag, err := tx.Exec(ctx, `
		WITH parsed AS (
			SELECT ST_Multi(ST_CollectionExtract(ST_MakeValid(ST_SetSRID(ST_GeomFromGeoJSON($8), 4326)), 3)) AS boundary
		)
		INSERT INTO administrative_regions (
			id, parent_id, country_code, level, code, slug, name, boundary,
			source, source_version, source_feature_id, source_license, is_active,
			created_at, updated_at
		)
		SELECT $1, $2, 'NG', $3, $4, $5, $6, parsed.boundary,
			$9, $10, $7, $11, TRUE, NOW(), NOW()
		FROM parsed
		WHERE parsed.boundary IS NOT NULL AND NOT ST_IsEmpty(parsed.boundary)
		ON CONFLICT (country_code, code) DO UPDATE SET
			parent_id = EXCLUDED.parent_id,
			slug = EXCLUDED.slug,
			name = EXCLUDED.name,
			boundary = EXCLUDED.boundary,
			source = EXCLUDED.source,
			source_version = EXCLUDED.source_version,
			source_feature_id = EXCLUDED.source_feature_id,
			source_license = EXCLUDED.source_license,
			is_active = TRUE,
			updated_at = NOW()
	`, input.ID, input.ParentID, input.Level, input.Code, input.Slug, input.Name,
		input.SourceFeatureID, string(input.Geometry), sourceName, sourceVersion, sourceLicense)
	if err != nil {
		return err
	}
	if commandTag.RowsAffected() != 1 {
		return fmt.Errorf("geometry was empty after validation")
	}
	return nil
}

func locateParentState(ctx context.Context, tx pgx.Tx, geometry json.RawMessage) (uuid.UUID, string, error) {
	var id uuid.UUID
	var code string
	err := tx.QueryRow(ctx, `
		WITH parsed AS (
			SELECT ST_Multi(ST_CollectionExtract(ST_MakeValid(ST_SetSRID(ST_GeomFromGeoJSON($1), 4326)), 3)) AS boundary
		)
		SELECT region.id, region.code
		FROM administrative_regions region, parsed
		WHERE region.country_code = 'NG'
		  AND region.level = 'state'
		  AND region.is_active
		  AND ST_Intersects(region.boundary, parsed.boundary)
		ORDER BY
			CASE WHEN ST_Covers(region.boundary, ST_PointOnSurface(parsed.boundary)) THEN 0 ELSE 1 END,
			ST_Area(ST_Intersection(region.boundary, parsed.boundary)) DESC,
			region.code
		LIMIT 1
	`, string(geometry)).Scan(&id, &code)
	if err != nil {
		return uuid.Nil, "", err
	}
	return id, code, nil
}

func regionBackfillSQL(table, leadingAssignment string) string {
	return fmt.Sprintf(`
		UPDATE %s location
		SET %s
			country_code = 'NG',
			state_region_id = state.id,
			lga_region_id = (
				SELECT lga.id
				FROM administrative_regions lga
				WHERE lga.parent_id = state.id
				  AND lga.level = 'lga'
				  AND lga.is_active
				  AND ST_Covers(lga.boundary, location.geog::geometry)
				ORDER BY lga.code
				LIMIT 1
			)
		FROM administrative_regions state
		WHERE location.geog IS NOT NULL
		  AND state.country_code = 'NG'
		  AND state.level = 'state'
		  AND state.is_active
		  AND ST_Covers(state.boundary, location.geog::geometry)
	`, table, leadingAssignment)
}

func decodeCollection(reader io.Reader, expectedType string, expectedCount int) (featureCollection, error) {
	var collection featureCollection
	decoder := json.NewDecoder(reader)
	if err := decoder.Decode(&collection); err != nil {
		return featureCollection{}, fmt.Errorf("decode %s GeoJSON: %w", expectedType, err)
	}
	if collection.Type != "FeatureCollection" {
		return featureCollection{}, fmt.Errorf("%s GeoJSON type is %q, expected FeatureCollection", expectedType, collection.Type)
	}
	if len(collection.Features) != expectedCount {
		return featureCollection{}, fmt.Errorf("%s feature count is %d, expected %d", expectedType, len(collection.Features), expectedCount)
	}
	for index, item := range collection.Features {
		if item.Type != "Feature" || len(item.Geometry) == 0 || string(item.Geometry) == "null" {
			return featureCollection{}, fmt.Errorf("%s feature %d is missing geometry", expectedType, index)
		}
		if item.Properties.ShapeType != expectedType {
			return featureCollection{}, fmt.Errorf("%s feature %d has shape type %q", expectedType, index, item.Properties.ShapeType)
		}
	}
	return collection, nil
}

func deterministicID(code string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("tellbook:administrative-region:"+strings.ToUpper(strings.TrimSpace(code))))
}

func slugify(value string) string {
	var builder strings.Builder
	for _, character := range strings.ToLower(strings.TrimSpace(value)) {
		if character <= unicode.MaxASCII {
			builder.WriteRune(character)
		}
	}
	return strings.Trim(nonSlugCharacters.ReplaceAllString(builder.String(), "-"), "-")
}

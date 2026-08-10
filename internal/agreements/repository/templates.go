package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"booking/go-server/internal/agreements/domain"
	"booking/go-server/internal/agreements/render"
	aiapi "booking/go-server/shared/ai_api"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type TemplateFamilyListFilter struct {
	Search          string
	Status          *domain.TemplateFamilyStatus
	Category        string
	BeforeUpdatedAt *time.Time
	BeforeID        *uuid.UUID
	Limit           int
}

type TemplateFamilyListItem struct {
	ID                        uuid.UUID
	Title                     string
	Description               string
	Category                  string
	Tags                      []string
	ConfirmationMethod        domain.ConfirmationMethod
	Status                    domain.TemplateFamilyStatus
	CurrentPublishedVersionID *uuid.UUID
	DraftVersionID            *uuid.UUID
	DraftRevision             *int64
	ServiceUsage              int
	AgreementUsage            int
	UpdatedAt                 time.Time
}

type TemplateFamilyDetails struct {
	Family           domain.TemplateFamily
	Draft            *domain.TemplateVersion
	CurrentPublished *domain.TemplateVersion
	PreviousVersions []domain.TemplateVersion
	ServiceUsage     int
	AgreementUsage   int
}

type UpdateTemplateDraftParams struct {
	ClientID           uuid.UUID
	FamilyID           uuid.UUID
	ExpectedRevision   int64
	Title              string
	Description        string
	Category           string
	Tags               []string
	ConfirmationMethod domain.ConfirmationMethod
	Document           aiapi.DocumentSchema
}

func (r *Repository) ListClientTemplateFamilies(
	ctx context.Context,
	clientID uuid.UUID,
	filter TemplateFamilyListFilter,
) ([]TemplateFamilyListItem, error) {
	if r == nil || r.db == nil || clientID == uuid.Nil {
		return nil, errors.New("invalid template family list")
	}
	if filter.Limit <= 0 || filter.Limit > 50 {
		filter.Limit = 20
	}
	status := ""
	if filter.Status != nil {
		if _, err := domain.ParseTemplateFamilyStatus(string(*filter.Status)); err != nil {
			return nil, err
		}
		status = string(*filter.Status)
	}
	if (filter.BeforeUpdatedAt == nil) != (filter.BeforeID == nil) {
		return nil, errors.New("template list cursor is incomplete")
	}
	rows, err := r.db.Query(ctx, `
		SELECT f.id, f.title, f.description, f.category, f.tags,
		       f.confirmation_method, f.status, f.current_published_version_id,
		       d.id, d.revision,
		       (SELECT COUNT(*) FROM services s WHERE s.client_id = $1 AND s.agreement_template_family_id = f.id),
		       (SELECT COUNT(*) FROM agreement_instances ai WHERE ai.client_id = $1 AND ai.template_family_id = f.id),
		       f.updated_at
		FROM agreement_template_families f
		LEFT JOIN agreement_template_versions d
		  ON d.family_id = f.id AND d.state = 'draft'
		WHERE f.owner_type = 'client' AND f.client_id = $1
		  AND ($2 = '' OR f.status = $2)
		  AND ($3 = '' OR LOWER(f.category) = LOWER($3))
		  AND ($4 = '' OR f.title ILIKE '%' || $4 || '%' OR f.description ILIKE '%' || $4 || '%')
		  AND ($5::timestamptz IS NULL OR (f.updated_at, f.id) < ($5, $6::uuid))
		ORDER BY f.updated_at DESC, f.id DESC
		LIMIT $7
	`, clientID, status, strings.TrimSpace(filter.Category), strings.TrimSpace(filter.Search), filter.BeforeUpdatedAt, filter.BeforeID, filter.Limit)
	if err != nil {
		return nil, fmt.Errorf("list agreement template families: %w", err)
	}
	defer rows.Close()

	items := make([]TemplateFamilyListItem, 0, filter.Limit)
	for rows.Next() {
		var item TemplateFamilyListItem
		var method, familyStatus string
		if err := rows.Scan(
			&item.ID, &item.Title, &item.Description, &item.Category, &item.Tags,
			&method, &familyStatus, &item.CurrentPublishedVersionID,
			&item.DraftVersionID, &item.DraftRevision, &item.ServiceUsage,
			&item.AgreementUsage, &item.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan agreement template family: %w", err)
		}
		parsedMethod, err := domain.ParseConfirmationMethod(method)
		if err != nil {
			return nil, err
		}
		parsedStatus, err := domain.ParseTemplateFamilyStatus(familyStatus)
		if err != nil {
			return nil, err
		}
		item.ConfirmationMethod = parsedMethod
		item.Status = parsedStatus
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate agreement template families: %w", err)
	}
	return items, nil
}

func (r *Repository) GetClientTemplateFamily(ctx context.Context, clientID, familyID uuid.UUID) (TemplateFamilyDetails, error) {
	if r == nil || r.db == nil || clientID == uuid.Nil || familyID == uuid.Nil {
		return TemplateFamilyDetails{}, errors.New("invalid template family lookup")
	}
	family, err := loadClientTemplateFamily(ctx, r.db, clientID, familyID, false)
	if err != nil {
		return TemplateFamilyDetails{}, err
	}
	rows, err := r.db.Query(ctx, `
		SELECT id, family_id, version_number, state, document_schema, used_variable_keys,
		       schema_version, renderer_version, source_kind, source_pdf_r2_key,
		       source_pdf_file_name, template_schema_hash, review_warnings, revision,
		       published_at, created_by_client_id, created_at, updated_at
		FROM agreement_template_versions
		WHERE family_id = $1
		ORDER BY version_number DESC
	`, familyID)
	if err != nil {
		return TemplateFamilyDetails{}, fmt.Errorf("list agreement template versions: %w", err)
	}
	defer rows.Close()

	details := TemplateFamilyDetails{Family: family, PreviousVersions: make([]domain.TemplateVersion, 0)}
	for rows.Next() {
		version, err := scanTemplateVersion(rows, family.ConfirmationMethod)
		if err != nil {
			return TemplateFamilyDetails{}, err
		}
		switch {
		case version.State == domain.TemplateVersionDraft:
			details.Draft = &version
		case family.CurrentPublishedVersionID != nil && version.ID == *family.CurrentPublishedVersionID:
			details.CurrentPublished = &version
		default:
			details.PreviousVersions = append(details.PreviousVersions, version)
		}
	}
	if err := rows.Err(); err != nil {
		return TemplateFamilyDetails{}, fmt.Errorf("iterate agreement template versions: %w", err)
	}
	if err := r.db.QueryRow(ctx, `
		SELECT
		  (SELECT COUNT(*) FROM services WHERE client_id = $1 AND agreement_template_family_id = $2),
		  (SELECT COUNT(*) FROM agreement_instances WHERE client_id = $1 AND template_family_id = $2)
	`, clientID, familyID).Scan(&details.ServiceUsage, &details.AgreementUsage); err != nil {
		return TemplateFamilyDetails{}, fmt.Errorf("load agreement template usage: %w", err)
	}
	return details, nil
}

func (r *Repository) UpdateClientTemplateDraft(ctx context.Context, params UpdateTemplateDraftParams) (domain.TemplateVersion, error) {
	if r == nil || r.db == nil || params.ClientID == uuid.Nil || params.FamilyID == uuid.Nil || params.ExpectedRevision <= 0 {
		return domain.TemplateVersion{}, errors.New("invalid template draft update")
	}
	params.Title = strings.TrimSpace(params.Title)
	params.Description = strings.TrimSpace(params.Description)
	params.Category = strings.TrimSpace(params.Category)
	if params.Title == "" || params.Category == "" {
		return domain.TemplateVersion{}, errors.New("template title and category are required")
	}
	if _, err := domain.ParseConfirmationMethod(string(params.ConfirmationMethod)); err != nil {
		return domain.TemplateVersion{}, err
	}
	if err := domain.ValidateDocument(params.Document, params.ConfirmationMethod, domain.AgreementVariableKeySet()); err != nil {
		return domain.TemplateVersion{}, fmt.Errorf("validate agreement draft: %w", err)
	}
	documentJSON, err := json.Marshal(params.Document)
	if err != nil {
		return domain.TemplateVersion{}, fmt.Errorf("encode agreement draft: %w", err)
	}
	hash, err := domain.TemplateSchemaHash(params.Document, params.ConfirmationMethod)
	if err != nil {
		return domain.TemplateVersion{}, err
	}

	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.TemplateVersion{}, fmt.Errorf("begin template draft update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	family, err := loadClientTemplateFamily(ctx, tx, params.ClientID, params.FamilyID, true)
	if err != nil {
		return domain.TemplateVersion{}, err
	}
	if family.Status == domain.TemplateFamilyArchived {
		return domain.TemplateVersion{}, ErrInvalidTransition
	}
	if family.CurrentPublishedVersionID != nil && family.ConfirmationMethod != params.ConfirmationMethod {
		return domain.TemplateVersion{}, ErrInvalidTransition
	}

	var versionID uuid.UUID
	var revision int64
	err = tx.QueryRow(ctx, `
		SELECT id, revision
		FROM agreement_template_versions
		WHERE family_id = $1 AND state = 'draft'
		FOR UPDATE
	`, params.FamilyID).Scan(&versionID, &revision)
	if errors.Is(err, pgx.ErrNoRows) {
		if family.CurrentPublishedVersionID == nil {
			return domain.TemplateVersion{}, ErrNotFound
		}
		versionID, revision, err = clonePublishedVersionAsDraft(ctx, tx, family, params.ExpectedRevision)
		if err != nil {
			return domain.TemplateVersion{}, err
		}
	} else if err != nil {
		return domain.TemplateVersion{}, fmt.Errorf("lock agreement template draft: %w", err)
	}
	if revision != params.ExpectedRevision {
		return domain.TemplateVersion{}, ErrConflict
	}

	command, err := tx.Exec(ctx, `
		UPDATE agreement_template_versions
		SET document_schema = $2, used_variable_keys = $3, schema_version = $4,
		    renderer_version = $5, template_schema_hash = $6, review_warnings = '[]',
		    revision = revision + 1, updated_at = NOW()
		WHERE id = $1 AND family_id = $7 AND state = 'draft' AND revision = $8
	`, versionID, documentJSON, params.Document.VariableKeys(), aiapi.AgreementDocumentSchemaVersion,
		render.RendererVersion, hash, params.FamilyID, params.ExpectedRevision)
	if err != nil {
		return domain.TemplateVersion{}, fmt.Errorf("update agreement template draft: %w", err)
	}
	if command.RowsAffected() != 1 {
		return domain.TemplateVersion{}, ErrConflict
	}
	command, err = tx.Exec(ctx, `
		UPDATE agreement_template_families
		SET title = $2, description = $3, category = $4, tags = $5,
		    confirmation_method = $6, updated_at = NOW()
		WHERE id = $1 AND client_id = $7 AND owner_type = 'client'
	`, params.FamilyID, params.Title, params.Description, params.Category,
		normalizeTags(params.Tags), params.ConfirmationMethod, params.ClientID)
	if err != nil {
		return domain.TemplateVersion{}, fmt.Errorf("update agreement template metadata: %w", err)
	}
	if command.RowsAffected() != 1 {
		return domain.TemplateVersion{}, ErrNotFound
	}

	version, err := loadTemplateVersion(ctx, tx, versionID, params.ConfirmationMethod)
	if err != nil {
		return domain.TemplateVersion{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.TemplateVersion{}, fmt.Errorf("commit template draft update: %w", err)
	}
	return version, nil
}

func (r *Repository) PublishClientTemplateDraft(ctx context.Context, clientID, familyID uuid.UUID) (domain.TemplateVersion, error) {
	if r == nil || r.db == nil || clientID == uuid.Nil || familyID == uuid.Nil {
		return domain.TemplateVersion{}, errors.New("invalid template publication")
	}
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.TemplateVersion{}, fmt.Errorf("begin template publication: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	family, err := loadClientTemplateFamily(ctx, tx, clientID, familyID, true)
	if err != nil {
		return domain.TemplateVersion{}, err
	}
	if family.Status == domain.TemplateFamilyArchived {
		return domain.TemplateVersion{}, ErrInvalidTransition
	}
	var draftID uuid.UUID
	if err := tx.QueryRow(ctx, `
		SELECT id FROM agreement_template_versions
		WHERE family_id = $1 AND state = 'draft'
		FOR UPDATE
	`, familyID).Scan(&draftID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.TemplateVersion{}, ErrInvalidTransition
		}
		return domain.TemplateVersion{}, fmt.Errorf("lock agreement template draft: %w", err)
	}
	draft, err := loadTemplateVersion(ctx, tx, draftID, family.ConfirmationMethod)
	if err != nil {
		return domain.TemplateVersion{}, err
	}
	if draft.Document == nil || draft.TemplateSchemaHash == "" {
		return domain.TemplateVersion{}, ErrInvalidTransition
	}
	if err := draft.Validate(family.ConfirmationMethod); err != nil {
		return domain.TemplateVersion{}, err
	}
	if family.CurrentPublishedVersionID != nil {
		command, err := tx.Exec(ctx, `
			UPDATE agreement_template_versions
			SET state = 'retired', updated_at = NOW()
			WHERE id = $1 AND family_id = $2 AND state = 'published'
		`, *family.CurrentPublishedVersionID, familyID)
		if err != nil {
			return domain.TemplateVersion{}, fmt.Errorf("retire previous agreement template version: %w", err)
		}
		if command.RowsAffected() != 1 {
			return domain.TemplateVersion{}, ErrConflict
		}
	}
	command, err := tx.Exec(ctx, `
		UPDATE agreement_template_versions
		SET state = 'published', published_at = NOW(), updated_at = NOW()
		WHERE id = $1 AND family_id = $2 AND state = 'draft'
	`, draftID, familyID)
	if err != nil {
		return domain.TemplateVersion{}, fmt.Errorf("publish agreement template version: %w", err)
	}
	if command.RowsAffected() != 1 {
		return domain.TemplateVersion{}, ErrConflict
	}
	command, err = tx.Exec(ctx, `
		UPDATE agreement_template_families
		SET status = 'published', current_published_version_id = $2,
		    archived_at = NULL, updated_at = NOW()
		WHERE id = $1 AND client_id = $3 AND owner_type = 'client'
	`, familyID, draftID, clientID)
	if err != nil {
		return domain.TemplateVersion{}, fmt.Errorf("publish agreement template family: %w", err)
	}
	if command.RowsAffected() != 1 {
		return domain.TemplateVersion{}, ErrNotFound
	}
	published, err := loadTemplateVersion(ctx, tx, draftID, family.ConfirmationMethod)
	if err != nil {
		return domain.TemplateVersion{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.TemplateVersion{}, fmt.Errorf("commit template publication: %w", err)
	}
	return published, nil
}

func (r *Repository) DuplicateClientTemplateFamily(ctx context.Context, clientID, familyID uuid.UUID) (uuid.UUID, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return uuid.Nil, fmt.Errorf("begin agreement template duplication: %w", err)
	}
	defer tx.Rollback(ctx)
	family, err := loadClientTemplateFamily(ctx, tx, clientID, familyID, true)
	if err != nil {
		return uuid.Nil, err
	}
	var sourceVersionID uuid.UUID
	if err := tx.QueryRow(ctx, `
		SELECT id
		FROM agreement_template_versions
		WHERE family_id = $1 AND document_schema IS NOT NULL
		ORDER BY CASE state WHEN 'draft' THEN 0 WHEN 'published' THEN 1 ELSE 2 END,
		         version_number DESC
		LIMIT 1
	`, familyID).Scan(&sourceVersionID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, ErrInvalidTransition
		}
		return uuid.Nil, fmt.Errorf("load agreement template duplication source: %w", err)
	}
	newFamilyID := uuid.New()
	newVersionID := uuid.New()
	if _, err := tx.Exec(ctx, `
		INSERT INTO agreement_template_families (
			id, client_id, owner_type, title, description, category, tags,
			confirmation_method, status, source_family_id, created_by_client_id,
			created_at, updated_at
		) VALUES ($1,$2,'client',$3,$4,$5,$6,$7,'draft',$8,$2,NOW(),NOW())
	`, newFamilyID, clientID, "Copy of "+family.Title, family.Description, family.Category,
		family.Tags, family.ConfirmationMethod, family.ID); err != nil {
		return uuid.Nil, fmt.Errorf("insert duplicated agreement template family: %w", err)
	}
	command, err := tx.Exec(ctx, `
		INSERT INTO agreement_template_versions (
			id, family_id, version_number, state, document_schema, used_variable_keys,
			schema_version, renderer_version, source_kind, template_schema_hash,
			review_warnings, revision, created_by_client_id, created_at, updated_at
		)
		SELECT $1,$2,1,'draft',document_schema,used_variable_keys,schema_version,
		       renderer_version,'library_copy',template_schema_hash,review_warnings,1,$3,NOW(),NOW()
		FROM agreement_template_versions WHERE id = $4 AND family_id = $5
	`, newVersionID, newFamilyID, clientID, sourceVersionID, familyID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("insert duplicated agreement template draft: %w", err)
	}
	if command.RowsAffected() != 1 {
		return uuid.Nil, ErrConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return uuid.Nil, fmt.Errorf("commit agreement template duplication: %w", err)
	}
	return newFamilyID, nil
}

func (r *Repository) ArchiveClientTemplateFamily(ctx context.Context, clientID, familyID uuid.UUID) error {
	command, err := r.db.Exec(ctx, `
		UPDATE agreement_template_families f
		SET status = 'archived', archived_at = NOW(), updated_at = NOW()
		WHERE f.id = $1 AND f.client_id = $2 AND f.owner_type = 'client'
		  AND f.status <> 'archived'
		  AND NOT EXISTS (
			SELECT 1 FROM services s
			WHERE s.client_id = $2 AND s.agreement_template_family_id = f.id
		  )
	`, familyID, clientID)
	if err != nil {
		return fmt.Errorf("archive agreement template: %w", err)
	}
	if command.RowsAffected() != 1 {
		return ErrInvalidTransition
	}
	return nil
}

func (r *Repository) RestoreClientTemplateFamily(ctx context.Context, clientID, familyID uuid.UUID) error {
	command, err := r.db.Exec(ctx, `
		UPDATE agreement_template_families
		SET status = CASE WHEN current_published_version_id IS NULL THEN 'draft' ELSE 'published' END,
		    archived_at = NULL, updated_at = NOW()
		WHERE id = $1 AND client_id = $2 AND owner_type = 'client' AND status = 'archived'
	`, familyID, clientID)
	if err != nil {
		return fmt.Errorf("restore agreement template: %w", err)
	}
	if command.RowsAffected() != 1 {
		return ErrInvalidTransition
	}
	return nil
}

func (r *Repository) DeleteClientTemplateDraftFamily(ctx context.Context, clientID, familyID uuid.UUID) ([]string, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin agreement template deletion: %w", err)
	}
	defer tx.Rollback(ctx)
	var lockedID uuid.UUID
	if err := tx.QueryRow(ctx, `
		SELECT id FROM agreement_template_families
		WHERE id = $1 AND client_id = $2 AND owner_type = 'client'
		  AND current_published_version_id IS NULL
		FOR UPDATE
	`, familyID, clientID).Scan(&lockedID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrInvalidTransition
		}
		return nil, fmt.Errorf("lock agreement template for deletion: %w", err)
	}
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT v.source_pdf_r2_key
		FROM agreement_template_versions v
		WHERE v.family_id = $1 AND v.source_pdf_r2_key <> ''
	`, familyID)
	if err != nil {
		return nil, fmt.Errorf("load agreement template source files: %w", err)
	}
	keys := make([]string, 0)
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan agreement template source file: %w", err)
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterate agreement template source files: %w", err)
	}
	rows.Close()
	command, err := tx.Exec(ctx, `
		DELETE FROM agreement_template_families f
		WHERE f.id = $1 AND f.client_id = $2 AND f.owner_type = 'client'
		  AND f.current_published_version_id IS NULL
		  AND NOT EXISTS (SELECT 1 FROM services s WHERE s.agreement_template_family_id = f.id)
		  AND NOT EXISTS (SELECT 1 FROM agreement_instances ai WHERE ai.template_family_id = f.id)
	`, familyID, clientID)
	if err != nil {
		return nil, fmt.Errorf("delete agreement template draft: %w", err)
	}
	if command.RowsAffected() != 1 {
		return nil, ErrInvalidTransition
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit agreement template deletion: %w", err)
	}
	return keys, nil
}

type templateFamilyQuery interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func loadClientTemplateFamily(
	ctx context.Context,
	query templateFamilyQuery,
	clientID,
	familyID uuid.UUID,
	forUpdate bool,
) (domain.TemplateFamily, error) {
	statement := `
		SELECT id, client_id, owner_type, title, description, category, tags,
		       confirmation_method, status, current_published_version_id,
		       source_family_id, created_by_client_id, created_at, updated_at, archived_at
		FROM agreement_template_families
		WHERE id = $1 AND client_id = $2 AND owner_type = 'client'
	`
	if forUpdate {
		statement += " FOR UPDATE"
	}
	var family domain.TemplateFamily
	var ownerType, method, status string
	var ownerClientID uuid.UUID
	if err := query.QueryRow(ctx, statement, familyID, clientID).Scan(
		&family.ID, &ownerClientID, &ownerType, &family.Title, &family.Description,
		&family.Category, &family.Tags, &method, &status, &family.CurrentPublishedVersionID,
		&family.SourceFamilyID, &family.CreatedByClientID, &family.CreatedAt, &family.UpdatedAt,
		&family.ArchivedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.TemplateFamily{}, ErrNotFound
		}
		return domain.TemplateFamily{}, fmt.Errorf("load agreement template family: %w", err)
	}
	parsedOwner, err := domain.ParseOwnerType(ownerType)
	if err != nil {
		return domain.TemplateFamily{}, err
	}
	parsedMethod, err := domain.ParseConfirmationMethod(method)
	if err != nil {
		return domain.TemplateFamily{}, err
	}
	parsedStatus, err := domain.ParseTemplateFamilyStatus(status)
	if err != nil {
		return domain.TemplateFamily{}, err
	}
	family.Owner = domain.TemplateOwner{Type: parsedOwner, ClientID: &ownerClientID}
	family.ConfirmationMethod = parsedMethod
	family.Status = parsedStatus
	if err := family.Validate(); err != nil {
		return domain.TemplateFamily{}, err
	}
	return family, nil
}

func loadTemplateVersion(
	ctx context.Context,
	query templateFamilyQuery,
	versionID uuid.UUID,
	method domain.ConfirmationMethod,
) (domain.TemplateVersion, error) {
	row := query.QueryRow(ctx, `
		SELECT id, family_id, version_number, state, document_schema, used_variable_keys,
		       schema_version, renderer_version, source_kind, source_pdf_r2_key,
		       source_pdf_file_name, template_schema_hash, review_warnings, revision,
		       published_at, created_by_client_id, created_at, updated_at
		FROM agreement_template_versions
		WHERE id = $1
	`, versionID)
	version, err := scanTemplateVersion(row, method)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.TemplateVersion{}, ErrNotFound
	}
	return version, err
}

func scanTemplateVersion(row rowScanner, method domain.ConfirmationMethod) (domain.TemplateVersion, error) {
	var version domain.TemplateVersion
	var state, sourceKind string
	var documentJSON []byte
	if err := row.Scan(
		&version.ID, &version.FamilyID, &version.VersionNumber, &state, &documentJSON,
		&version.UsedVariableKeys, &version.SchemaVersion, &version.RendererVersion,
		&sourceKind, &version.SourcePDFR2Key, &version.SourcePDFFileName,
		&version.TemplateSchemaHash, &version.ReviewWarnings, &version.Revision,
		&version.PublishedAt, &version.CreatedByClientID, &version.CreatedAt, &version.UpdatedAt,
	); err != nil {
		return domain.TemplateVersion{}, err
	}
	parsedState, err := domain.ParseTemplateVersionState(state)
	if err != nil {
		return domain.TemplateVersion{}, err
	}
	parsedSource, err := domain.ParseTemplateSourceKind(sourceKind)
	if err != nil {
		return domain.TemplateVersion{}, err
	}
	version.State = parsedState
	version.SourceKind = parsedSource
	if len(documentJSON) > 0 && string(documentJSON) != "null" {
		var document aiapi.DocumentSchema
		if err := json.Unmarshal(documentJSON, &document); err != nil {
			return domain.TemplateVersion{}, fmt.Errorf("decode agreement template document: %w", err)
		}
		version.Document = &document
	}
	if err := version.Validate(method); err != nil {
		return domain.TemplateVersion{}, err
	}
	return version, nil
}

func clonePublishedVersionAsDraft(
	ctx context.Context,
	tx pgx.Tx,
	family domain.TemplateFamily,
	expectedRevision int64,
) (uuid.UUID, int64, error) {
	var publishedRevision int64
	if err := tx.QueryRow(ctx, `
		SELECT revision
		FROM agreement_template_versions
		WHERE id = $1 AND family_id = $2 AND state = 'published'
		FOR UPDATE
	`, *family.CurrentPublishedVersionID, family.ID).Scan(&publishedRevision); err != nil {
		return uuid.Nil, 0, fmt.Errorf("lock current agreement template version: %w", err)
	}
	if publishedRevision != expectedRevision {
		return uuid.Nil, 0, ErrConflict
	}
	versionID := uuid.New()
	command, err := tx.Exec(ctx, `
		INSERT INTO agreement_template_versions (
			id, family_id, version_number, state, document_schema, used_variable_keys,
			schema_version, renderer_version, source_kind, source_pdf_r2_key,
			source_pdf_file_name, template_schema_hash, review_warnings, revision,
			created_by_client_id, created_at, updated_at
		)
		SELECT $1, family_id,
		       (SELECT COALESCE(MAX(version_number), 0) + 1 FROM agreement_template_versions WHERE family_id = $2),
		       'draft', document_schema, used_variable_keys, schema_version, renderer_version,
		       source_kind, source_pdf_r2_key, source_pdf_file_name, template_schema_hash,
		       review_warnings, revision, $3, NOW(), NOW()
		FROM agreement_template_versions
		WHERE id = $4 AND family_id = $2 AND state = 'published'
	`, versionID, family.ID, *family.Owner.ClientID, *family.CurrentPublishedVersionID)
	if err != nil {
		return uuid.Nil, 0, fmt.Errorf("create editable agreement template draft: %w", err)
	}
	if command.RowsAffected() != 1 {
		return uuid.Nil, 0, ErrConflict
	}
	return versionID, publishedRevision, nil
}

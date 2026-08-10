package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"booking/go-server/internal/agreements/domain"
	"booking/go-server/internal/agreements/render"
	agreementseed "booking/go-server/internal/agreements/seed"
	aiapi "booking/go-server/shared/ai_api"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type SystemTemplateListItem struct {
	ID                 uuid.UUID
	Title              string
	Description        string
	Category           string
	Tags               []string
	ConfirmationMethod domain.ConfirmationMethod
}

func (r *Repository) SyncSystemTemplates(ctx context.Context) error {
	templates, err := agreementseed.SystemTemplates()
	if err != nil {
		return err
	}
	for _, template := range templates {
		if err := r.syncSystemTemplate(ctx, template); err != nil {
			return err
		}
	}
	return nil
}

func (r *Repository) syncSystemTemplate(ctx context.Context, template agreementseed.SystemTemplate) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin system agreement template sync: %w", err)
	}
	defer tx.Rollback(ctx)
	familyID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("tellbook:agreement-family:"+template.Key))
	if _, err := tx.Exec(ctx, `
		INSERT INTO agreement_template_families (
			id, client_id, owner_type, title, description, category, tags,
			confirmation_method, status, created_at, updated_at
		) VALUES ($1,NULL,'system',$2,$3,$4,$5,$6,'published',NOW(),NOW())
		ON CONFLICT (id) DO UPDATE SET
			title = EXCLUDED.title, description = EXCLUDED.description,
			category = EXCLUDED.category, tags = EXCLUDED.tags, updated_at = NOW()
	`, familyID, template.Title, template.Description, template.Category,
		template.Tags, template.ConfirmationMethod); err != nil {
		return fmt.Errorf("upsert system agreement template family: %w", err)
	}
	var currentID *uuid.UUID
	var currentHash string
	if err := tx.QueryRow(ctx, `
		SELECT f.current_published_version_id, COALESCE(v.template_schema_hash, '')
		FROM agreement_template_families f
		LEFT JOIN agreement_template_versions v ON v.id = f.current_published_version_id
		WHERE f.id = $1 AND f.owner_type = 'system'
		FOR UPDATE OF f
	`, familyID).Scan(&currentID, &currentHash); err != nil {
		return fmt.Errorf("load current system agreement template: %w", err)
	}
	if currentID != nil && currentHash == template.TemplateSchemaHash {
		return tx.Commit(ctx)
	}
	documentJSON, err := json.Marshal(template.Document)
	if err != nil {
		return fmt.Errorf("encode system agreement template: %w", err)
	}
	versionID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("tellbook:agreement-version:"+template.Key+":"+template.TemplateSchemaHash))
	if currentID != nil {
		if _, err := tx.Exec(ctx, `
			UPDATE agreement_template_versions SET state = 'retired', updated_at = NOW()
			WHERE id = $1 AND family_id = $2 AND state = 'published'
		`, *currentID, familyID); err != nil {
			return fmt.Errorf("retire previous system agreement template: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO agreement_template_versions (
			id, family_id, version_number, state, document_schema, used_variable_keys,
			schema_version, renderer_version, source_kind, template_schema_hash,
			review_warnings, revision, published_at, created_at, updated_at
		) VALUES (
			$1,$2,(SELECT COALESCE(MAX(version_number),0)+1 FROM agreement_template_versions WHERE family_id=$2),
			'published',$3,$4,$5,$6,'system_seed',$7,'[]',1,NOW(),NOW(),NOW()
		)
		ON CONFLICT (id) DO UPDATE SET state = 'published', published_at = NOW(), updated_at = NOW()
	`, versionID, familyID, documentJSON, template.UsedVariableKeys,
		template.Document.SchemaVersion, render.RendererVersion, template.TemplateSchemaHash); err != nil {
		return fmt.Errorf("insert system agreement template version: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE agreement_template_families
		SET current_published_version_id = $2, status = 'published', updated_at = NOW()
		WHERE id = $1
	`, familyID, versionID); err != nil {
		return fmt.Errorf("publish system agreement template: %w", err)
	}
	return tx.Commit(ctx)
}

func (r *Repository) ListSystemTemplateFamilies(ctx context.Context, search, category string) ([]SystemTemplateListItem, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id, title, description, category, tags, confirmation_method
		FROM agreement_template_families
		WHERE owner_type = 'system' AND status = 'published'
		  AND ($1 = '' OR title ILIKE '%' || $1 || '%' OR description ILIKE '%' || $1 || '%')
		  AND ($2 = '' OR LOWER(category) = LOWER($2))
		ORDER BY category ASC, title ASC
	`, strings.TrimSpace(search), strings.TrimSpace(category))
	if err != nil {
		return nil, fmt.Errorf("list system agreement templates: %w", err)
	}
	defer rows.Close()
	items := make([]SystemTemplateListItem, 0)
	for rows.Next() {
		var item SystemTemplateListItem
		var method string
		if err := rows.Scan(&item.ID, &item.Title, &item.Description, &item.Category, &item.Tags, &method); err != nil {
			return nil, err
		}
		item.ConfirmationMethod, err = domain.ParseConfirmationMethod(method)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *Repository) CopySystemTemplate(ctx context.Context, clientID, familyID uuid.UUID) (uuid.UUID, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return uuid.Nil, fmt.Errorf("begin system agreement template copy: %w", err)
	}
	defer tx.Rollback(ctx)
	var title, description, category, method string
	var tags []string
	var documentJSON []byte
	var usedKeys []string
	var schemaVersion, rendererVersion int
	var hash string
	if err := tx.QueryRow(ctx, `
		SELECT f.title, f.description, f.category, f.tags, f.confirmation_method,
		       v.document_schema, v.used_variable_keys, v.schema_version,
		       v.renderer_version, v.template_schema_hash
		FROM agreement_template_families f
		INNER JOIN agreement_template_versions v ON v.id = f.current_published_version_id
		WHERE f.id = $1 AND f.owner_type = 'system' AND f.status = 'published'
	`, familyID).Scan(
		&title, &description, &category, &tags, &method, &documentJSON,
		&usedKeys, &schemaVersion, &rendererVersion, &hash,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, ErrNotFound
		}
		return uuid.Nil, fmt.Errorf("load system agreement template: %w", err)
	}
	confirmationMethod, err := domain.ParseConfirmationMethod(method)
	if err != nil {
		return uuid.Nil, err
	}
	var document aiapi.DocumentSchema
	if err := json.Unmarshal(documentJSON, &document); err != nil {
		return uuid.Nil, err
	}
	if err := domain.ValidateDocument(document, confirmationMethod, domain.AgreementVariableKeySet()); err != nil {
		return uuid.Nil, err
	}
	newFamilyID := uuid.New()
	if _, err := tx.Exec(ctx, `
		INSERT INTO agreement_template_families (
			id, client_id, owner_type, title, description, category, tags,
			confirmation_method, status, source_family_id, created_by_client_id,
			created_at, updated_at
		) VALUES ($1,$2,'client',$3,$4,$5,$6,$7,'draft',$8,$2,NOW(),NOW())
	`, newFamilyID, clientID, title, description, category, tags, confirmationMethod, familyID); err != nil {
		return uuid.Nil, fmt.Errorf("insert copied agreement template family: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO agreement_template_versions (
			id, family_id, version_number, state, document_schema, used_variable_keys,
			schema_version, renderer_version, source_kind, template_schema_hash,
			review_warnings, revision, created_by_client_id, created_at, updated_at
		) VALUES ($1,$2,1,'draft',$3,$4,$5,$6,'library_copy',$7,'[]',1,$8,NOW(),NOW())
	`, uuid.New(), newFamilyID, documentJSON, usedKeys, schemaVersion, rendererVersion, hash, clientID); err != nil {
		return uuid.Nil, fmt.Errorf("insert copied agreement template draft: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return uuid.Nil, err
	}
	return newFamilyID, nil
}

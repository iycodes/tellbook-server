package appdata

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// catalogDatabase lets existing aggregate operations participate in an outer
// transaction. Their Begin/Commit pairs become pgx savepoints, not independent commits.
type catalogDatabase interface {
	Begin(context.Context) (pgx.Tx, error)
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func (r *Repository) catalogDB() catalogDatabase {
	if r.catalogTx != nil {
		return r.catalogTx
	}
	return r.db
}
func (r *Repository) catalogIn(tx pgx.Tx) *Repository { return &Repository{db: r.db, catalogTx: tx} }

type CatalogError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *CatalogError) Is(target error) bool { return e.Code == "not_found" && target == ErrNotFound }

func (e *CatalogError) Error() string         { return e.Message }
func catalogError(code, message string) error { return &CatalogError{code, message} }

type CatalogActor struct {
	ProviderID       uuid.UUID
	GrantID          uuid.UUID
	Resource         string
	SecurityRevision int64
}
type CatalogReceipt struct {
	Replayed   bool            `json:"-"`
	ReceiptID  string          `json:"receipt_id"`
	Operation  string          `json:"operation"`
	ResourceID string          `json:"resource_id"`
	Revision   int64           `json:"revision,omitempty"`
	Data       json.RawMessage `json:"data"`
}

func CatalogScope(operation string) string {
	switch operation {
	case "get_connected_profile", "list_services", "get_service", "list_service_sections", "get_service_section", "get_service_setup_options":
		return "catalog.read"
	case "set_service_status":
		return "catalog.publish"
	case "delete_service", "delete_service_section":
		return "catalog.delete"
	default:
		return "catalog.write"
	}
}

// MutateCatalog is the shared REST/MCP boundary. A successful receipt and its
// changes are committed together; authorization is checked before replaying it.
func (h *Handler) MutateCatalog(ctx context.Context, actor CatalogActor, operation string, resourceID uuid.UUID, input json.RawMessage, revision int64, key string) (CatalogReceipt, error) {
	if strings.TrimSpace(key) == "" || len(key) > 200 {
		return CatalogReceipt{}, catalogError("idempotency_key_required", "Provide an idempotency key of 1 to 200 characters.")
	}
	var canonical any
	dec := json.NewDecoder(bytes.NewReader(input))
	dec.UseNumber()
	if err := dec.Decode(&canonical); err != nil {
		return CatalogReceipt{}, catalogError("invalid_request", "Provide a JSON object.")
	}
	if _, ok := canonical.(map[string]any); !ok {
		return CatalogReceipt{}, catalogError("invalid_request", "Provide a JSON object.")
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return CatalogReceipt{}, catalogError("invalid_request", "Provide one JSON object.")
	}
	raw, _ := json.Marshal(struct {
		Operation string
		ID        uuid.UUID
		Revision  int64
		Input     any
	}{operation, resourceID, revision, canonical})
	hash := sha256.Sum256(raw)
	tx, err := h.repo.db.Begin(ctx)
	if err != nil {
		return CatalogReceipt{}, err
	}
	defer tx.Rollback(ctx)
	var security int64
	if err = tx.QueryRow(ctx, `SELECT security_revision FROM clients WHERE id=$1 FOR UPDATE`, actor.ProviderID).Scan(&security); err != nil {
		return CatalogReceipt{}, catalogError("unauthorized", "Provider account is unavailable.")
	}
	if actor.SecurityRevision != security {
		return CatalogReceipt{}, catalogError("unauthorized", "Reconnect your Tellbook account.")
	}
	actorKey := "browser"
	if actor.GrantID != uuid.Nil {
		actorKey = actor.GrantID.String()
		var permitted bool
		err = tx.QueryRow(ctx, `SELECT $5=ANY(scopes) FROM integration_grants WHERE id=$1 AND provider_id=$2 AND resource=$3 AND security_revision=$4 AND revoked_at IS NULL AND expires_at>now() FOR SHARE`, actor.GrantID, actor.ProviderID, actor.Resource, security, CatalogScope(operation)).Scan(&permitted)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return CatalogReceipt{}, err
		}
		if !permitted {
			return CatalogReceipt{}, catalogError("insufficient_scope", "This connection does not permit this action. Reconnect with the required permission.")
		}
	}
	var previous []byte
	var previousHash []byte
	err = tx.QueryRow(ctx, `SELECT request_hash,result FROM catalog_mutation_receipts WHERE provider_id=$1 AND actor_key=$2 AND idempotency_key=$3`, actor.ProviderID, actorKey, key).Scan(&previousHash, &previous)
	if err == nil {
		if !bytes.Equal(previousHash, hash[:]) {
			return CatalogReceipt{}, catalogError("idempotency_conflict", "This idempotency key was already used for a different request.")
		}
		var receipt CatalogReceipt
		if err = json.Unmarshal(previous, &receipt); err != nil {
			return receipt, err
		}
		receipt.Replayed = true
		return receipt, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return CatalogReceipt{}, err
	}
	repo := h.repo.catalogIn(tx)
	sectionOp := strings.Contains(operation, "service_section") || operation == "reorder_section_services"
	if resourceID != uuid.Nil {
		if revision <= 0 {
			return CatalogReceipt{}, catalogError("revision_required", "Read this resource and submit its current revision before changing it.")
		}
		table := "services"
		if sectionOp {
			table = "service_sections"
		}
		var actual int64
		err = tx.QueryRow(ctx, `SELECT revision FROM `+table+` WHERE client_id=$1 AND id=$2 FOR UPDATE`, actor.ProviderID, resourceID).Scan(&actual)
		if errors.Is(err, pgx.ErrNoRows) {
			return CatalogReceipt{}, catalogError("not_found", "This resource was not found in your account.")
		}
		if err != nil {
			return CatalogReceipt{}, err
		}
		if revision != actual {
			return CatalogReceipt{}, catalogError("revision_conflict", "This resource changed. Read its latest state before trying again.")
		}
	}
	var result any
	var oldImage, newImage, category string
	switch operation {
	case "reorder_service_sections", "reorder_section_services", "reorder_uncategorized_services":
		var value ReorderItemsInput
		if err = strictCatalogDecode(input, &value); err != nil {
			return CatalogReceipt{}, err
		}
		orderedIDs, e := parseOrderedUUIDs(value.OrderedIDs)
		if e != nil {
			return CatalogReceipt{}, catalogError("invalid_request", e.Error())
		}
		table := "services"
		if operation == "reorder_service_sections" {
			table = "service_sections"
		}
		for _, id := range orderedIDs {
			expected := value.ExpectedRevisions[id.String()]
			if expected < 1 {
				return CatalogReceipt{}, catalogError("revision_required", "Supply the revision read for each reordered resource.")
			}
			var current int64
			if e = tx.QueryRow(ctx, `SELECT revision FROM `+table+` WHERE id=$1 AND client_id=$2 FOR UPDATE`, id, actor.ProviderID).Scan(&current); e != nil {
				return CatalogReceipt{}, catalogPublicError(e)
			}
			if current != expected {
				return CatalogReceipt{}, catalogError("revision_conflict", "The order changed. Reload before reordering.")
			}
		}
		switch operation {
		case "reorder_service_sections":
			err = repo.ReorderServiceSections(ctx, actor.ProviderID, orderedIDs)
		case "reorder_section_services":
			err = repo.ReorderSectionServices(ctx, actor.ProviderID, resourceID, orderedIDs)
		case "reorder_uncategorized_services":
			err = repo.ReorderUncategorizedServices(ctx, actor.ProviderID, orderedIDs)
		}
		result = map[string]any{"ordered_ids": value.OrderedIDs}
	case "create_service", "replace_service", "update_service", "set_service_status":
		var value CreateManagedServiceInput
		var previous *ManagedServiceItem
		if operation != "create_service" {
			item, e := repo.GetManagedServiceDetails(ctx, actor.ProviderID, resourceID)
			if e != nil {
				return CatalogReceipt{}, e
			}
			value = serviceInput(item)
			previous = &item
		}
		if operation == "create_service" || operation == "replace_service" {
			previousStatus := value.PublishStatus
			value = CreateManagedServiceInput{}
			if err = strictCatalogDecode(input, &value); err != nil {
				return CatalogReceipt{}, err
			}
			value.PublishStatus = previousStatus
			if operation == "create_service" {
				value.PublishStatus = "draft"
			}
		} else if operation == "set_service_status" {
			var status struct {
				Status string `json:"status"`
			}
			if err = strictCatalogDecode(input, &status); err != nil {
				return CatalogReceipt{}, err
			}
			if status.Status != "draft" && status.Status != "published" && status.Status != "paused" {
				return CatalogReceipt{}, catalogError("invalid_status", "Status must be draft, published, or paused.")
			}
			value.PublishStatus = status.Status
		} else {
			var changes map[string]any
			decoder := json.NewDecoder(bytes.NewReader(input))
			decoder.UseNumber()
			_ = decoder.Decode(&changes)
			if _, ok := changes["publish_status"]; ok {
				return CatalogReceipt{}, catalogError("invalid_request", "Use set_service_status to change publication.")
			}
			if _, ok := changes["wizard_draft_id"]; ok {
				return CatalogReceipt{}, catalogError("invalid_request", "Wizard drafts are managed in Tellbook.")
			}
			baseBytes, _ := json.Marshal(value)
			var base map[string]any
			decoder = json.NewDecoder(bytes.NewReader(baseBytes))
			decoder.UseNumber()
			_ = decoder.Decode(&base)
			if err = mergeCatalogPatch(base, changes); err != nil {
				return CatalogReceipt{}, err
			}
			merged, _ := json.Marshal(base)
			if err = strictCatalogDecode(merged, &value); err != nil {
				return CatalogReceipt{}, err
			}
		}
		if !h.validOptionalOwnedPublicImage(actor.ProviderID, value.ImageURL, "services") {
			return CatalogReceipt{}, catalogError("invalid_image", "Choose an image already uploaded to this Tellbook account.")
		}
		if actor.GrantID != uuid.Nil && (previous == nil || value.ImageURL != previous.ImageURL) {
			if err = requireCatalogImageReference(ctx, repo, actor.ProviderID, value.ImageURL); err != nil {
				return CatalogReceipt{}, err
			}
		}
		if _, err = loadConfiguredCurrencyCodeTx(ctx, tx, actor.ProviderID); err != nil {
			return CatalogReceipt{}, catalogError("market_required", "Complete your business country and currency in Tellbook first.")
		}
		var item ManagedServiceItem
		if operation == "create_service" {
			item, err = repo.CreateManagedService(ctx, actor.ProviderID, value)
		} else {
			item, err = repo.saveManagedService(ctx, actor.ProviderID, resourceID, value, false, previous)
		}
		result = item
		resourceID = catalogID(item.ID)
		oldImage = item.replacedImageURL
		newImage = value.ImageURL
		category = "services"
	case "duplicate_service":
		if err = strictCatalogDecode(input, &struct{}{}); err != nil {
			return CatalogReceipt{}, err
		}
		result, err = repo.DuplicateManagedService(ctx, actor.ProviderID, resourceID)
	case "set_service_visibility":
		var value UpdateManagedServiceVisibilityInput
		if _, ok := canonical.(map[string]any)["is_hidden"].(bool); !ok {
			return CatalogReceipt{}, catalogError("invalid_request", "Supply is_hidden explicitly.")
		}
		if err = strictCatalogDecode(input, &value); err != nil {
			return CatalogReceipt{}, err
		}
		result, err = repo.UpdateManagedServiceVisibility(ctx, actor.ProviderID, resourceID, value.IsHidden)
	case "delete_service":
		if err = strictCatalogDecode(input, &struct{}{}); err != nil {
			return CatalogReceipt{}, err
		}
		oldImage, err = repo.DeleteManagedService(ctx, actor.ProviderID, resourceID)
		category = "services"
		result = map[string]any{"id": resourceID.String(), "deleted": true}
	case "create_service_section", "replace_service_section", "update_service_section":
		var value CreateServiceSectionInput
		previousImage := ""
		if operation != "create_service_section" {
			var details ServiceSectionDetailsResponse
			details, err = repo.GetServiceSectionDetails(ctx, actor.ProviderID, resourceID)
			if err != nil {
				return CatalogReceipt{}, err
			}
			value = CreateServiceSectionInput{Name: details.Section.Name, Description: details.Section.Description, CoverImageURL: details.Section.CoverImageURL}
			previousImage = value.CoverImageURL
		}
		if operation == "update_service_section" {
			baseBytes, _ := json.Marshal(value)
			var base, patch map[string]any
			_ = json.Unmarshal(baseBytes, &base)
			_ = json.Unmarshal(input, &patch)
			if err = mergeCatalogPatch(base, patch); err != nil {
				return CatalogReceipt{}, err
			}
			input, _ = json.Marshal(base)
		}
		if err = strictCatalogDecode(input, &value); err != nil {
			return CatalogReceipt{}, err
		}
		if !h.validOptionalOwnedPublicImage(actor.ProviderID, value.CoverImageURL, "sections") {
			return CatalogReceipt{}, catalogError("invalid_image", "Choose an image uploaded to this account.")
		}
		if actor.GrantID != uuid.Nil && value.CoverImageURL != previousImage {
			if err = requireCatalogImageReference(ctx, repo, actor.ProviderID, value.CoverImageURL); err != nil {
				return CatalogReceipt{}, err
			}
		}
		var item ServiceSectionItem
		if operation == "create_service_section" {
			item, err = repo.CreateServiceSection(ctx, actor.ProviderID, value)
		} else {
			item, err = repo.UpdateServiceSection(ctx, actor.ProviderID, resourceID, UpdateServiceSectionInput(value))
		}
		if err == nil {
			resourceID = uuid.MustParse(item.ID)
			details, e := repo.GetServiceSectionDetails(ctx, actor.ProviderID, resourceID)
			err = e
			result = details.Section
		}
		oldImage = item.replacedImageURL
		newImage = value.CoverImageURL
		category = "sections"
	case "delete_service_section":
		var value DeleteServiceSectionInput
		if err = strictCatalogDecode(input, &value); err != nil {
			return CatalogReceipt{}, err
		}
		if value.Mode != "uncategorized" && value.Mode != "move" {
			return CatalogReceipt{}, catalogError("invalid_request", "Choose uncategorized or move; services are preserved.")
		}
		oldImage, err = repo.DeleteServiceSection(ctx, actor.ProviderID, resourceID, value)
		category = "sections"
		result = map[string]any{"id": resourceID.String(), "deleted": true}
	default:
		return CatalogReceipt{}, catalogError("unsupported_operation", "This operation is not supported.")
	}
	if err != nil {
		return CatalogReceipt{}, catalogPublicError(err)
	}
	receipt := CatalogReceipt{ReceiptID: uuid.NewString(), Operation: operation, ResourceID: resourceID.String()}
	if item, ok := result.(ManagedServiceItem); ok {
		receipt.ResourceID = item.ID
		receipt.Revision = item.Revision
	}
	if item, ok := result.(ServiceSectionItem); ok {
		receipt.Revision = item.Revision
	}
	receipt.Data, err = json.Marshal(result)
	if err != nil {
		return receipt, err
	}
	encoded, _ := json.Marshal(receipt)
	_, err = tx.Exec(ctx, `INSERT INTO catalog_mutation_receipts(id,provider_id,grant_id,actor_key,idempotency_key,operation,request_hash,result) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, receipt.ReceiptID, actor.ProviderID, nullableGrant(actor.GrantID), actorKey, key, operation, hash[:], encoded)
	if err != nil {
		return receipt, err
	}
	if err = tx.Commit(ctx); err != nil {
		return receipt, err
	}
	h.deleteReplacedPublicImage(ctx, actor.ProviderID, oldImage, newImage, category)
	return receipt, nil
}

func requireCatalogImageReference(ctx context.Context, repo *Repository, provider uuid.UUID, image string) error {
	image = strings.TrimSpace(image)
	if image == "" {
		return nil
	}
	var exists bool
	if err := repo.catalogDB().QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM services WHERE client_id=$1 AND image_url=$2 UNION ALL SELECT 1 FROM service_sections WHERE client_id=$1 AND cover_image_url=$2)`, provider, image).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return catalogError("invalid_image", "Connected apps can reuse images already assigned to Tellbook services or sections.")
	}
	return nil
}
func nullableGrant(id uuid.UUID) any {
	if id == uuid.Nil {
		return nil
	}
	return id
}
func strictCatalogDecode(raw []byte, value any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return catalogError("invalid_request", err.Error())
	}
	return nil
}
func mergeCatalogPatch(base, patch map[string]any) error {
	for key, value := range patch {
		old := base[key]
		if value == nil && key != "max_travel_distance_meters" {
			return catalogError("invalid_request", "Use the existing empty value to clear "+key)
		}
		if child, ok := value.(map[string]any); ok {
			target, ok := old.(map[string]any)
			if !ok {
				return catalogError("invalid_request", "Invalid object: "+key)
			}
			if err := mergeCatalogPatch(target, child); err != nil {
				return err
			}
		} else {
			base[key] = value
		}
	}
	return nil
}
func serviceInput(item ManagedServiceItem) CreateManagedServiceInput {
	return CreateManagedServiceInput{SectionID: item.SectionID, ServiceName: item.Name, Description: item.Description, Badge: item.Badge, DurationMinutes: item.DurationMinutes, Pricing: item.Pricing, Fulfillment: item.Fulfillment, Availability: item.Availability, ShortNoticeRules: item.ShortNoticeRules, VirtualDelivery: item.VirtualDelivery, CancellationPolicy: item.CancellationPolicy, LatenessPolicy: item.LatenessPolicy, AgreementTemplateFamilyID: item.AgreementTemplateFamilyID, AgreementTiming: item.AgreementTiming, StandaloneSignatureRequired: item.StandaloneSignatureRequired, Instructions: item.Instructions, PublishStatus: item.Status, ImageURL: item.ImageURL}
}
func catalogPublicError(err error) error {
	var public *CatalogError
	if errors.As(err, &public) {
		return public
	}
	if errors.Is(err, ErrNotFound) || errors.Is(err, pgx.ErrNoRows) {
		return catalogError("not_found", "This resource was not found in your account.")
	}
	if errors.Is(err, ErrServiceLimitReached) || errors.Is(err, ErrServiceSectionLimitReached) {
		return catalogError("collection_limit", "Your account has reached the limit for this collection.")
	}
	if errors.Is(err, ErrMarketNotConfigured) {
		return catalogError("market_required", "Complete your business country and currency in Tellbook first.")
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		if pgErr.Code == "23503" {
			return catalogError("service_in_use", "This service is referenced by a booking quote or proposal. Pause it instead.")
		}
		return catalogError("operation_failed", "The operation could not be completed.")
	}
	if errors.Unwrap(err) != nil {
		return catalogError("operation_failed", "The operation could not be completed.")
	}
	return catalogError("validation_failed", err.Error())
}

func (h *Handler) ReadCatalog(ctx context.Context, providerID uuid.UUID, operation string, id uuid.UUID) (any, error) {
	switch operation {
	case "list_services":
		return h.repo.ListManagedServices(ctx, providerID)
	case "list_service_sections":
		return h.repo.ListServiceSections(ctx, providerID)
	case "get_service", "get_service_section":
		tx, err := h.repo.db.Begin(ctx)
		if err != nil {
			return nil, err
		}
		defer tx.Rollback(ctx)
		table := "services"
		if operation == "get_service_section" {
			table = "service_sections"
		}
		var owned uuid.UUID
		if err = tx.QueryRow(ctx, `SELECT id FROM `+table+` WHERE client_id=$1 AND id=$2 FOR SHARE`, providerID, id).Scan(&owned); err != nil {
			return nil, catalogPublicError(err)
		}
		repo := h.repo.catalogIn(tx)
		if operation == "get_service" {
			return repo.GetManagedServiceDetails(ctx, providerID, id)
		}
		return repo.GetServiceSectionDetails(ctx, providerID, id)
	default:
		return nil, fmt.Errorf("unsupported catalog read")
	}
}

func catalogID(raw string) uuid.UUID { id, _ := uuid.Parse(raw); return id }

func (h *Handler) readManagedServiceForREST(ctx context.Context, provider, id uuid.UUID) (ManagedServiceItem, error) {
	value, err := h.ReadCatalog(ctx, provider, "get_service", id)
	if err != nil {
		return ManagedServiceItem{}, err
	}
	return value.(ManagedServiceItem), nil
}
func (h *Handler) readSectionForREST(ctx context.Context, provider, id uuid.UUID) (ServiceSectionDetailsResponse, error) {
	value, err := h.ReadCatalog(ctx, provider, "get_service_section", id)
	if err != nil {
		return ServiceSectionDetailsResponse{}, err
	}
	return value.(ServiceSectionDetailsResponse), nil
}

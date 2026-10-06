package main

import (
	agreementrender "booking/go-server/internal/agreements/render"
	agreementseed "booking/go-server/internal/agreements/seed"
	"booking/go-server/internal/appdata"
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func seedSetup(ctx context.Context, pool *pgxpool.Pool, accounts []reviewer) error {
	repo := appdata.NewRepository(pool)
	templates, err := agreementseed.SystemTemplates()
	if err != nil {
		return err
	}
	for _, account := range accounts {
		hours, err := repo.GetBusinessHours(ctx, account.ID)
		if err != nil {
			return err
		}
		if len(hours.Items) == 0 {
			items := []appdata.BusinessHoursWindow{}
			for day := 1; day <= 5; day++ {
				items = append(items, appdata.BusinessHoursWindow{DayOfWeek: day, StartTime: "09:00", EndTime: "17:00"})
			}
			if _, err = repo.UpdateBusinessHours(ctx, account.ID, appdata.UpdateBusinessHoursInput{Items: items}); err != nil {
				return err
			}
		}
		_, err = pool.Exec(ctx, `INSERT INTO business_locations(id,client_id,label,formatted_address,latitude,longitude,address_source,resolution_status,country_code,locality,timezone,is_primary,is_active) SELECT $1,$2,'Synthetic studio','Synthetic review address',6.4,3.4,'current_location','coordinates_resolved','NG','Synthetic city','Africa/Lagos',true,true WHERE NOT EXISTS(SELECT 1 FROM business_locations WHERE client_id=$2)`, uuid.New(), account.ID)
		if err != nil {
			return err
		}
		var exists bool
		if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agreement_template_families WHERE client_id=$1 AND status='published')`, account.ID).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		template := templates[0]
		family, version := uuid.New(), uuid.New()
		document, _ := json.Marshal(template.Document)
		tx, err := pool.Begin(ctx)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO agreement_template_families(id,client_id,owner_type,title,description,category,tags,confirmation_method,status,created_by_client_id) VALUES($1,$2,'client','Synthetic review agreement',$3,$4,$5,$6,'published',$2)`, family, account.ID, template.Description, template.Category, template.Tags, template.ConfirmationMethod)
		if err == nil {
			_, err = tx.Exec(ctx, `INSERT INTO agreement_template_versions(id,family_id,version_number,state,document_schema,used_variable_keys,schema_version,renderer_version,source_kind,template_schema_hash,revision,published_at,created_by_client_id) VALUES($1,$2,1,'published',$3,$4,$5,$6,'system_seed',$7,1,now(),$8)`, version, family, document, template.UsedVariableKeys, template.Document.SchemaVersion, agreementrender.RendererVersion, template.TemplateSchemaHash, account.ID)
		}
		if err == nil {
			_, err = tx.Exec(ctx, `UPDATE agreement_template_families SET current_published_version_id=$2 WHERE id=$1`, family, version)
		}
		if err != nil {
			tx.Rollback(ctx)
			return err
		}
		if err = tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}

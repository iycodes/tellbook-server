-- migrate:up
CREATE OR REPLACE FUNCTION catalog_membership_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  -- Ordinary service edits set section_id too. Only actual membership or order
  -- changes invalidate section revisions and their dependent UI preconditions.
  IF TG_OP = 'UPDATE' THEN
    IF NEW.section_id IS NOT DISTINCT FROM OLD.section_id
       AND NEW.sort_order IS NOT DISTINCT FROM OLD.sort_order THEN
      RETURN NULL;
    END IF;
  END IF;
  IF TG_OP <> 'INSERT' THEN UPDATE service_sections SET updated_at=clock_timestamp() WHERE id=OLD.section_id; END IF;
  IF TG_OP <> 'DELETE' AND (TG_OP='INSERT' OR NEW.section_id IS DISTINCT FROM OLD.section_id) THEN
    UPDATE service_sections SET updated_at=clock_timestamp() WHERE id=NEW.section_id;
  END IF;
  RETURN NULL;
END $$;

-- migrate:down
CREATE OR REPLACE FUNCTION catalog_membership_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP <> 'INSERT' THEN UPDATE service_sections SET updated_at=clock_timestamp() WHERE id=OLD.section_id; END IF;
  IF TG_OP <> 'DELETE' AND (TG_OP='INSERT' OR NEW.section_id IS DISTINCT FROM OLD.section_id) THEN
    UPDATE service_sections SET updated_at=clock_timestamp() WHERE id=NEW.section_id;
  END IF;
  RETURN NULL;
END $$;

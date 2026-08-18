-- migrate:up
DROP INDEX IF EXISTS public.service_wizard_drafts_one_new_per_client_idx;

-- migrate:down
CREATE UNIQUE INDEX service_wizard_drafts_one_new_per_client_idx
    ON public.service_wizard_drafts (client_id)
    WHERE service_id IS NULL;

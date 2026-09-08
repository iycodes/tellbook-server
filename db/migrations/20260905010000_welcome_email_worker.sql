-- migrate:up

CREATE TABLE welcome_email_templates (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    audience text NOT NULL CHECK (audience IN ('provider','marketplace_customer')),
    version integer NOT NULL CHECK (version > 0),
    name text NOT NULL CHECK (char_length(btrim(name)) BETWEEN 1 AND 120),
    subject_template text NOT NULL CHECK (char_length(subject_template) BETWEEN 1 AND 300),
    html_template text NOT NULL CHECK (char_length(html_template) BETWEEN 1 AND 200000),
    text_template text NOT NULL CHECK (char_length(text_template) BETWEEN 1 AND 100000),
    status text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','active','archived')),
    activated_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT NOW(),
    updated_at timestamptz NOT NULL DEFAULT NOW(),
    UNIQUE (audience,version),
    CHECK ((status='active' AND activated_at IS NOT NULL) OR status<>'active')
);

CREATE UNIQUE INDEX welcome_email_templates_one_active_audience_idx
    ON welcome_email_templates (audience) WHERE status='active';

CREATE TABLE welcome_email_jobs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    idempotency_key text NOT NULL UNIQUE CHECK (char_length(idempotency_key) BETWEEN 1 AND 200),
    audience text NOT NULL CHECK (audience IN ('provider','marketplace_customer')),
    provider_client_id uuid REFERENCES clients(id) ON DELETE CASCADE,
    marketplace_customer_id uuid REFERENCES marketplace_customers(id) ON DELETE CASCADE,
    template_id uuid NOT NULL REFERENCES welcome_email_templates(id) ON DELETE RESTRICT,
    template_version integer NOT NULL CHECK (template_version > 0),
    recipient_email text NOT NULL CHECK (char_length(recipient_email) BETWEEN 3 AND 320),
    recipient_name text NOT NULL DEFAULT '' CHECK (char_length(recipient_name) <= 200),
    subject text NOT NULL CHECK (char_length(subject) BETWEEN 1 AND 300),
    html_body text NOT NULL CHECK (char_length(html_body) BETWEEN 1 AND 200000),
    text_body text NOT NULL CHECK (char_length(text_body) BETWEEN 1 AND 100000),
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','processing','retry','accepted','failed','manual_review')),
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0 AND attempt_count <= 8),
    next_attempt_at timestamptz NOT NULL DEFAULT NOW(),
    lease_owner text NOT NULL DEFAULT '',
    lease_expires_at timestamptz,
    last_error_code text NOT NULL DEFAULT '' CHECK (char_length(last_error_code) <= 100),
    accepted_at timestamptz,
    completed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT NOW(),
    updated_at timestamptz NOT NULL DEFAULT NOW(),
    CHECK (
        (audience='provider' AND provider_client_id IS NOT NULL AND marketplace_customer_id IS NULL)
        OR
        (audience='marketplace_customer' AND provider_client_id IS NULL AND marketplace_customer_id IS NOT NULL)
    ),
    CHECK ((status='processing' AND lease_owner<>'' AND lease_expires_at IS NOT NULL) OR status<>'processing'),
    CHECK ((status='accepted' AND accepted_at IS NOT NULL AND completed_at IS NOT NULL) OR status<>'accepted'),
    CHECK ((status='failed' AND completed_at IS NOT NULL) OR status<>'failed')
);

CREATE INDEX welcome_email_jobs_due_idx
    ON welcome_email_jobs (next_attempt_at,created_at,id)
    WHERE status IN ('pending','retry');

CREATE INDEX welcome_email_jobs_expired_lease_idx
    ON welcome_email_jobs (lease_expires_at,id)
    WHERE status='processing';

CREATE INDEX welcome_email_jobs_terminal_retention_idx
    ON welcome_email_jobs (completed_at,id)
    WHERE status IN ('accepted','failed');

CREATE FUNCTION notify_welcome_email_job() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    PERFORM pg_notify('tellbook_worker_core','welcome_email');
    RETURN NEW;
END;
$$;

CREATE TRIGGER welcome_email_jobs_wake
    AFTER INSERT ON welcome_email_jobs
    FOR EACH STATEMENT EXECUTE FUNCTION notify_welcome_email_job();

INSERT INTO welcome_email_templates (
    audience,version,name,subject_template,html_template,text_template,status,activated_at
) VALUES
(
    'provider',1,'Provider welcome','Welcome to Tellbook, {{name}}',
    '<!doctype html><html><body style="margin:0;background:#f7f7f5;font-family:Arial,sans-serif;color:#171714"><div style="max-width:600px;margin:0 auto;padding:40px 24px"><div style="background:#fff;border:1px solid #e8e8e3;border-radius:20px;padding:36px"><h1 style="margin:0 0 16px;font-size:28px">Welcome to Tellbook, {{name}}</h1><p style="margin:0 0 16px;line-height:1.6">Your provider account is ready. Tellbook gives you one place to manage services, bookings, customers, payments, and conversations.</p><p style="margin:0;line-height:1.6">Start by completing your business profile and publishing your first service.</p></div></div></body></html>',
    E'Welcome to Tellbook, {{name}}\n\nYour provider account is ready. Tellbook gives you one place to manage services, bookings, customers, payments, and conversations.\n\nStart by completing your business profile and publishing your first service.',
    'active',NOW()
),
(
    'marketplace_customer',1,'Marketplace customer welcome','Welcome to Tellbook, {{name}}',
    '<!doctype html><html><body style="margin:0;background:#f7f7f5;font-family:Arial,sans-serif;color:#171714"><div style="max-width:600px;margin:0 auto;padding:40px 24px"><div style="background:#fff;border:1px solid #e8e8e3;border-radius:20px;padding:36px"><h1 style="margin:0 0 16px;font-size:28px">Welcome to Tellbook, {{name}}</h1><p style="margin:0 0 16px;line-height:1.6">Your account is ready. You can now discover trusted service providers, manage bookings, save favourites, and keep every conversation in one place.</p><p style="margin:0;line-height:1.6">We are glad to have you here.</p></div></div></body></html>',
    E'Welcome to Tellbook, {{name}}\n\nYour account is ready. You can now discover trusted service providers, manage bookings, save favourites, and keep every conversation in one place.\n\nWe are glad to have you here.',
    'active',NOW()
);

-- migrate:down

DROP TRIGGER IF EXISTS welcome_email_jobs_wake ON welcome_email_jobs;
DROP FUNCTION IF EXISTS notify_welcome_email_job();
DROP TABLE IF EXISTS welcome_email_jobs;
DROP TABLE IF EXISTS welcome_email_templates;

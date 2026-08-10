SET statement_timeout = 0;
SET lock_timeout = 0;
SET idle_in_transaction_session_timeout = 0;
SET client_encoding = 'UTF8';
SET standard_conforming_strings = on;
SELECT pg_catalog.set_config('search_path', '', false);
SET check_function_bodies = false;
SET xmloption = content;
SET client_min_messages = warning;
SET row_security = off;

--
-- Name: notify_payment_status_change(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.notify_payment_status_change() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    PERFORM pg_notify('tellbook_payment_status', NEW.public_token);
    RETURN NEW;
END;
$$;


SET default_tablespace = '';

SET default_table_access_method = heap;

--
-- Name: agreement_acceptances; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.agreement_acceptances (
    agreement_id uuid NOT NULL,
    method text NOT NULL,
    signer_name text DEFAULT ''::text NOT NULL,
    signature_png bytea DEFAULT '\x'::bytea NOT NULL,
    signature_sha256 text DEFAULT ''::text NOT NULL,
    accepted_at timestamp with time zone NOT NULL,
    resolved_terms_hash text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT agreement_acceptances_evidence_check CHECK ((((method = 'confirmation'::text) AND (signer_name = ''::text) AND (octet_length(signature_png) = 0) AND (signature_sha256 = ''::text)) OR ((method = 'signature'::text) AND (btrim(signer_name) <> ''::text) AND (octet_length(signature_png) > 0) AND (signature_sha256 ~ '^[a-f0-9]{64}$'::text)))),
    CONSTRAINT agreement_acceptances_method_check CHECK ((method = ANY (ARRAY['confirmation'::text, 'signature'::text]))),
    CONSTRAINT agreement_acceptances_resolved_terms_hash_check CHECK ((resolved_terms_hash ~ '^[a-f0-9]{64}$'::text))
);


--
-- Name: agreement_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.agreement_events (
    id uuid NOT NULL,
    agreement_id uuid NOT NULL,
    event_type text NOT NULL,
    actor_type text NOT NULL,
    dedupe_key text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    occurred_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT agreement_events_actor_type_check CHECK ((actor_type = ANY (ARRAY['system'::text, 'business'::text, 'customer'::text]))),
    CONSTRAINT agreement_events_dedupe_key_check CHECK ((btrim(dedupe_key) <> ''::text)),
    CONSTRAINT agreement_events_event_type_check CHECK ((event_type = ANY (ARRAY['created'::text, 'sent'::text, 'delivery_failed'::text, 'viewed'::text, 'completed'::text, 'pdf_ready'::text, 'pdf_failed'::text, 'resent'::text, 'expired'::text, 'cancelled'::text]))),
    CONSTRAINT agreement_events_metadata_check CHECK ((jsonb_typeof(metadata) = 'object'::text))
);


--
-- Name: agreement_instances; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.agreement_instances (
    id uuid NOT NULL,
    client_id uuid NOT NULL,
    customer_id uuid,
    booking_id uuid,
    template_family_id uuid,
    template_version_id uuid,
    title_snapshot text NOT NULL,
    booking_summary_snapshot jsonb DEFAULT '{}'::jsonb NOT NULL,
    resolved_document_snapshot jsonb NOT NULL,
    schema_version_snapshot integer NOT NULL,
    renderer_version_snapshot integer NOT NULL,
    rendered_html_snapshot text NOT NULL,
    resolved_terms_hash text NOT NULL,
    confirmation_method text NOT NULL,
    timing text NOT NULL,
    status text NOT NULL,
    public_token_hash bytea NOT NULL,
    public_token_ciphertext bytea NOT NULL,
    public_token_nonce bytea NOT NULL,
    public_token_key_version text NOT NULL,
    sent_to_email text DEFAULT ''::text NOT NULL,
    personal_message_snapshot text DEFAULT ''::text NOT NULL,
    delivery_revision integer DEFAULT 0 NOT NULL,
    expires_at timestamp with time zone,
    completed_at timestamp with time zone,
    pdf_status text DEFAULT 'not_requested'::text NOT NULL,
    pdf_r2_key text DEFAULT ''::text NOT NULL,
    pdf_sha256 text DEFAULT ''::text NOT NULL,
    pdf_error_code text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT agreement_instances_booking_summary_snapshot_check CHECK ((jsonb_typeof(booking_summary_snapshot) = 'object'::text)),
    CONSTRAINT agreement_instances_completion_check CHECK ((((status = 'completed'::text) AND (completed_at IS NOT NULL)) OR ((status <> 'completed'::text) AND (completed_at IS NULL)))),
    CONSTRAINT agreement_instances_confirmation_method_check CHECK ((confirmation_method = ANY (ARRAY['confirmation'::text, 'signature'::text]))),
    CONSTRAINT agreement_instances_delivery_revision_check CHECK ((delivery_revision >= 0)),
    CONSTRAINT agreement_instances_pdf_status_check CHECK ((pdf_status = ANY (ARRAY['not_requested'::text, 'queued'::text, 'processing'::text, 'ready'::text, 'failed'::text]))),
    CONSTRAINT agreement_instances_public_token_hash_check CHECK ((octet_length(public_token_hash) = 32)),
    CONSTRAINT agreement_instances_public_token_key_version_check CHECK ((btrim(public_token_key_version) <> ''::text)),
    CONSTRAINT agreement_instances_rendered_html_snapshot_check CHECK ((btrim(rendered_html_snapshot) <> ''::text)),
    CONSTRAINT agreement_instances_renderer_version_snapshot_check CHECK ((renderer_version_snapshot > 0)),
    CONSTRAINT agreement_instances_resolved_document_snapshot_check CHECK ((jsonb_typeof(resolved_document_snapshot) = 'object'::text)),
    CONSTRAINT agreement_instances_resolved_terms_hash_check CHECK ((resolved_terms_hash ~ '^[a-f0-9]{64}$'::text)),
    CONSTRAINT agreement_instances_schema_version_snapshot_check CHECK ((schema_version_snapshot > 0)),
    CONSTRAINT agreement_instances_status_check CHECK ((status = ANY (ARRAY['draft'::text, 'awaiting_customer'::text, 'completed'::text, 'expired'::text, 'cancelled'::text]))),
    CONSTRAINT agreement_instances_template_snapshot_check CHECK ((((template_family_id IS NULL) AND (template_version_id IS NULL) AND (timing = 'manual'::text)) OR ((template_family_id IS NOT NULL) AND (template_version_id IS NOT NULL)))),
    CONSTRAINT agreement_instances_timing_check CHECK ((timing = ANY (ARRAY['before_payment'::text, 'after_payment'::text, 'manual'::text]))),
    CONSTRAINT agreement_instances_title_snapshot_check CHECK ((btrim(title_snapshot) <> ''::text))
);


--
-- Name: agreement_jobs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.agreement_jobs (
    id uuid NOT NULL,
    agreement_id uuid NOT NULL,
    kind text NOT NULL,
    dedupe_key text NOT NULL,
    status text DEFAULT 'queued'::text NOT NULL,
    attempt_count integer DEFAULT 0 NOT NULL,
    max_attempts integer DEFAULT 5 NOT NULL,
    run_at timestamp with time zone DEFAULT now() NOT NULL,
    lease_owner text DEFAULT ''::text NOT NULL,
    lease_expires_at timestamp with time zone,
    error_code text DEFAULT ''::text NOT NULL,
    error_message text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT agreement_jobs_attempt_count_check CHECK ((attempt_count >= 0)),
    CONSTRAINT agreement_jobs_dedupe_key_check CHECK ((btrim(dedupe_key) <> ''::text)),
    CONSTRAINT agreement_jobs_kind_check CHECK ((kind = ANY (ARRAY['render_completed_pdf'::text, 'send_agreement_email'::text, 'send_completed_email'::text]))),
    CONSTRAINT agreement_jobs_max_attempts_check CHECK ((max_attempts > 0)),
    CONSTRAINT agreement_jobs_status_check CHECK ((status = ANY (ARRAY['queued'::text, 'processing'::text, 'completed'::text, 'failed'::text])))
);


--
-- Name: agreement_template_families; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.agreement_template_families (
    id uuid NOT NULL,
    client_id uuid,
    owner_type text NOT NULL,
    title text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    category text NOT NULL,
    tags text[] DEFAULT '{}'::text[] NOT NULL,
    confirmation_method text NOT NULL,
    status text NOT NULL,
    current_published_version_id uuid,
    source_family_id uuid,
    created_by_client_id uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    archived_at timestamp with time zone,
    CONSTRAINT agreement_template_families_category_check CHECK ((btrim(category) <> ''::text)),
    CONSTRAINT agreement_template_families_confirmation_method_check CHECK ((confirmation_method = ANY (ARRAY['confirmation'::text, 'signature'::text]))),
    CONSTRAINT agreement_template_families_owner_check CHECK ((((owner_type = 'system'::text) AND (client_id IS NULL)) OR ((owner_type = 'client'::text) AND (client_id IS NOT NULL)))),
    CONSTRAINT agreement_template_families_owner_type_check CHECK ((owner_type = ANY (ARRAY['system'::text, 'client'::text]))),
    CONSTRAINT agreement_template_families_status_check CHECK ((status = ANY (ARRAY['draft'::text, 'published'::text, 'archived'::text]))),
    CONSTRAINT agreement_template_families_title_check CHECK ((btrim(title) <> ''::text))
);


--
-- Name: agreement_template_generation_jobs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.agreement_template_generation_jobs (
    id uuid NOT NULL,
    client_id uuid NOT NULL,
    family_id uuid NOT NULL,
    version_id uuid NOT NULL,
    input_kind text NOT NULL,
    input_json jsonb NOT NULL,
    status text NOT NULL,
    attempt_count integer DEFAULT 0 NOT NULL,
    max_attempts integer DEFAULT 3 NOT NULL,
    run_at timestamp with time zone DEFAULT now() NOT NULL,
    lease_owner text DEFAULT ''::text NOT NULL,
    lease_expires_at timestamp with time zone,
    error_code text DEFAULT ''::text NOT NULL,
    error_message text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    started_at timestamp with time zone,
    completed_at timestamp with time zone,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT agreement_template_generation_jobs_attempt_count_check CHECK ((attempt_count >= 0)),
    CONSTRAINT agreement_template_generation_jobs_input_json_check CHECK ((jsonb_typeof(input_json) = 'object'::text)),
    CONSTRAINT agreement_template_generation_jobs_input_kind_check CHECK ((input_kind = ANY (ARRAY['fields'::text, 'upload'::text]))),
    CONSTRAINT agreement_template_generation_jobs_max_attempts_check CHECK ((max_attempts > 0)),
    CONSTRAINT agreement_template_generation_jobs_status_check CHECK ((status = ANY (ARRAY['queued'::text, 'processing'::text, 'completed'::text, 'failed'::text])))
);


--
-- Name: agreement_template_versions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.agreement_template_versions (
    id uuid NOT NULL,
    family_id uuid NOT NULL,
    version_number integer NOT NULL,
    state text NOT NULL,
    document_schema jsonb,
    used_variable_keys text[] DEFAULT '{}'::text[] NOT NULL,
    schema_version integer NOT NULL,
    renderer_version integer NOT NULL,
    source_kind text NOT NULL,
    source_pdf_r2_key text DEFAULT ''::text NOT NULL,
    source_pdf_file_name text DEFAULT ''::text NOT NULL,
    template_schema_hash text DEFAULT ''::text NOT NULL,
    review_warnings jsonb DEFAULT '[]'::jsonb NOT NULL,
    revision bigint DEFAULT 1 NOT NULL,
    published_at timestamp with time zone,
    created_by_client_id uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT agreement_template_versions_document_check CHECK ((((document_schema IS NULL) AND (state = 'draft'::text) AND (template_schema_hash = ''::text) AND (cardinality(used_variable_keys) = 0)) OR ((document_schema IS NOT NULL) AND (template_schema_hash <> ''::text)))),
    CONSTRAINT agreement_template_versions_published_at_check CHECK ((((state = 'published'::text) AND (published_at IS NOT NULL)) OR (state <> 'published'::text))),
    CONSTRAINT agreement_template_versions_renderer_version_check CHECK ((renderer_version > 0)),
    CONSTRAINT agreement_template_versions_review_warnings_check CHECK ((jsonb_typeof(review_warnings) = 'array'::text)),
    CONSTRAINT agreement_template_versions_revision_check CHECK ((revision > 0)),
    CONSTRAINT agreement_template_versions_schema_version_check CHECK ((schema_version > 0)),
    CONSTRAINT agreement_template_versions_source_kind_check CHECK ((source_kind = ANY (ARRAY['ai'::text, 'upload'::text, 'library_copy'::text, 'system_seed'::text]))),
    CONSTRAINT agreement_template_versions_state_check CHECK ((state = ANY (ARRAY['draft'::text, 'published'::text, 'retired'::text]))),
    CONSTRAINT agreement_template_versions_version_number_check CHECK ((version_number > 0))
);


--
-- Name: auth_password_reset_tokens; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.auth_password_reset_tokens (
    id uuid NOT NULL,
    client_id uuid NOT NULL,
    token_hash bytea NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: auth_pending_registrations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.auth_pending_registrations (
    id uuid NOT NULL,
    full_name text NOT NULL,
    bio text DEFAULT ''::text NOT NULL,
    email text NOT NULL,
    password_hash text NOT NULL,
    cover_image_data_url text DEFAULT ''::text NOT NULL,
    cover_image_content_type text DEFAULT ''::text NOT NULL,
    token_hash bytea NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: auth_refresh_sessions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.auth_refresh_sessions (
    id uuid NOT NULL,
    client_id uuid NOT NULL,
    token_hash bytea NOT NULL,
    user_agent text DEFAULT ''::text NOT NULL,
    ip_address text DEFAULT ''::text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    last_used_at timestamp with time zone DEFAULT now() NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: automation_settings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.automation_settings (
    id uuid NOT NULL,
    client_id uuid NOT NULL,
    automation_key text NOT NULL,
    title text DEFAULT ''::text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    action_label text DEFAULT ''::text NOT NULL,
    enabled boolean DEFAULT false NOT NULL,
    config jsonb DEFAULT '{}'::jsonb NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: booking_quote_promotions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.booking_quote_promotions (
    booking_quote_id uuid NOT NULL,
    promotion_id uuid NOT NULL,
    customer_email_normalized text NOT NULL,
    code_used text DEFAULT ''::text NOT NULL,
    discount_amount_minor bigint NOT NULL,
    currency_code text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT booking_quote_promotions_amount_check CHECK ((discount_amount_minor > 0)),
    CONSTRAINT booking_quote_promotions_currency_check CHECK ((currency_code ~ '^[A-Z]{3}$'::text))
);


--
-- Name: booking_quotes; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.booking_quotes (
    id uuid NOT NULL,
    public_token text NOT NULL,
    client_id uuid NOT NULL,
    service_id uuid NOT NULL,
    booking_id uuid,
    service_title text NOT NULL,
    business_name text NOT NULL,
    service_image_url text DEFAULT ''::text NOT NULL,
    duration_minutes integer NOT NULL,
    appointment_start_at timestamp with time zone NOT NULL,
    appointment_end_at timestamp with time zone NOT NULL,
    occupied_start_at timestamp with time zone NOT NULL,
    occupied_end_at timestamp with time zone NOT NULL,
    prep_time_minutes integer DEFAULT 0 NOT NULL,
    buffer_time_minutes integer DEFAULT 0 NOT NULL,
    timezone text NOT NULL,
    fulfillment_mode text NOT NULL,
    location_label text NOT NULL,
    provider_location_label text DEFAULT ''::text NOT NULL,
    provider_place_id text,
    provider_latitude numeric(9,6),
    provider_longitude numeric(9,6),
    customer_location_label text DEFAULT ''::text NOT NULL,
    customer_place_id text,
    customer_latitude numeric(9,6),
    customer_longitude numeric(9,6),
    travel_distance_meters integer,
    virtual_delivery_label text DEFAULT ''::text NOT NULL,
    virtual_join_url text,
    virtual_instructions text,
    country_code text NOT NULL,
    currency_code text NOT NULL,
    locale text NOT NULL,
    cancellation_policy text DEFAULT ''::text NOT NULL,
    lateness_policy text DEFAULT ''::text NOT NULL,
    base_service_amount_minor bigint NOT NULL,
    promotion_id uuid,
    discount_name text DEFAULT ''::text NOT NULL,
    discount_source text DEFAULT ''::text NOT NULL,
    discount_code text DEFAULT ''::text NOT NULL,
    discount_type text DEFAULT ''::text NOT NULL,
    discount_percentage_bps bigint DEFAULT 0 NOT NULL,
    discount_value_minor bigint DEFAULT 0 NOT NULL,
    discount_amount_minor bigint DEFAULT 0 NOT NULL,
    short_notice_rule_id uuid,
    short_notice_threshold_minutes integer,
    short_notice_surcharge_type text DEFAULT ''::text NOT NULL,
    short_notice_surcharge_amount_minor bigint DEFAULT 0 NOT NULL,
    short_notice_surcharge_percentage_bps integer DEFAULT 0 NOT NULL,
    short_notice_fee_minor bigint DEFAULT 0 NOT NULL,
    travel_fee_minor bigint DEFAULT 0 NOT NULL,
    discounted_service_amount_minor bigint NOT NULL,
    total_amount_minor bigint NOT NULL,
    deposit_amount_minor bigint NOT NULL,
    remaining_amount_minor bigint NOT NULL,
    customer_email_normalized text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    consumed_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    customer_name_snapshot text DEFAULT ''::text NOT NULL,
    customer_phone_snapshot text DEFAULT ''::text NOT NULL,
    booking_notes_snapshot text DEFAULT ''::text NOT NULL,
    agreement_template_family_id_snapshot uuid,
    agreement_template_version_id_snapshot uuid,
    agreement_title_snapshot text DEFAULT ''::text NOT NULL,
    agreement_booking_summary_snapshot jsonb DEFAULT '{}'::jsonb NOT NULL,
    agreement_resolved_document_snapshot jsonb,
    agreement_schema_version_snapshot integer,
    agreement_renderer_version_snapshot integer,
    agreement_rendered_html_snapshot text DEFAULT ''::text NOT NULL,
    agreement_resolved_terms_hash_snapshot text DEFAULT ''::text NOT NULL,
    agreement_confirmation_method_snapshot text DEFAULT ''::text NOT NULL,
    agreement_timing_snapshot text DEFAULT ''::text NOT NULL,
    standalone_signature_required_snapshot boolean DEFAULT false NOT NULL,
    CONSTRAINT booking_quotes_agreement_snapshot_shape_check CHECK ((((agreement_template_family_id_snapshot IS NULL) AND (agreement_template_version_id_snapshot IS NULL) AND (agreement_title_snapshot = ''::text) AND (agreement_resolved_document_snapshot IS NULL) AND (agreement_schema_version_snapshot IS NULL) AND (agreement_renderer_version_snapshot IS NULL) AND (agreement_rendered_html_snapshot = ''::text) AND (agreement_resolved_terms_hash_snapshot = ''::text) AND (agreement_confirmation_method_snapshot = ''::text) AND (agreement_timing_snapshot = ''::text)) OR ((agreement_template_family_id_snapshot IS NOT NULL) AND (agreement_template_version_id_snapshot IS NOT NULL) AND (btrim(agreement_title_snapshot) <> ''::text) AND (agreement_resolved_document_snapshot IS NOT NULL) AND (agreement_schema_version_snapshot > 0) AND (agreement_renderer_version_snapshot > 0) AND (btrim(agreement_rendered_html_snapshot) <> ''::text) AND (agreement_resolved_terms_hash_snapshot ~ '^[a-f0-9]{64}$'::text) AND (agreement_confirmation_method_snapshot = ANY (ARRAY['confirmation'::text, 'signature'::text])) AND (agreement_timing_snapshot = ANY (ARRAY['before_payment'::text, 'after_payment'::text])) AND (standalone_signature_required_snapshot = false)))),
    CONSTRAINT booking_quotes_consumption_check CHECK ((((consumed_at IS NULL) AND (booking_id IS NULL)) OR ((consumed_at IS NOT NULL) AND (booking_id IS NOT NULL)))),
    CONSTRAINT booking_quotes_expiry_check CHECK ((expires_at > created_at)),
    CONSTRAINT booking_quotes_fulfillment_mode_check CHECK ((fulfillment_mode = ANY (ARRAY['provider_location'::text, 'customer_location'::text, 'virtual'::text]))),
    CONSTRAINT booking_quotes_market_check CHECK (((country_code ~ '^[A-Z]{2}$'::text) AND (currency_code ~ '^[A-Z]{3}$'::text))),
    CONSTRAINT booking_quotes_nonnegative_check CHECK (((prep_time_minutes >= 0) AND (buffer_time_minutes >= 0) AND (duration_minutes > 0) AND (base_service_amount_minor >= 0) AND (discount_amount_minor >= 0) AND (short_notice_fee_minor >= 0) AND (travel_fee_minor >= 0) AND (discounted_service_amount_minor >= 0) AND (total_amount_minor >= 0) AND (deposit_amount_minor >= 0) AND (remaining_amount_minor >= 0))),
    CONSTRAINT booking_quotes_time_check CHECK (((occupied_start_at <= appointment_start_at) AND (appointment_start_at < appointment_end_at) AND (appointment_end_at <= occupied_end_at))),
    CONSTRAINT booking_quotes_total_check CHECK (((((discounted_service_amount_minor + short_notice_fee_minor) + travel_fee_minor) = total_amount_minor) AND ((deposit_amount_minor + remaining_amount_minor) = total_amount_minor)))
);


--
-- Name: booking_standalone_signatures; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.booking_standalone_signatures (
    booking_id uuid NOT NULL,
    signer_name text NOT NULL,
    signature_png bytea NOT NULL,
    signature_sha256 text NOT NULL,
    accepted_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT booking_standalone_signatures_signature_png_check CHECK ((octet_length(signature_png) > 0)),
    CONSTRAINT booking_standalone_signatures_signature_sha256_check CHECK ((signature_sha256 ~ '^[a-f0-9]{64}$'::text)),
    CONSTRAINT booking_standalone_signatures_signer_name_check CHECK ((btrim(signer_name) <> ''::text))
);


--
-- Name: bookings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.bookings (
    id uuid NOT NULL,
    client_id uuid NOT NULL,
    customer_id uuid NOT NULL,
    service_id uuid,
    title text DEFAULT ''::text NOT NULL,
    stylist_name text DEFAULT ''::text NOT NULL,
    source text DEFAULT ''::text NOT NULL,
    status text DEFAULT ''::text NOT NULL,
    payment_status text DEFAULT ''::text NOT NULL,
    agreement_status text DEFAULT ''::text NOT NULL,
    start_at timestamp with time zone NOT NULL,
    end_at timestamp with time zone NOT NULL,
    timezone text DEFAULT 'Africa/Lagos'::text NOT NULL,
    base_service_amount_minor bigint DEFAULT 0 NOT NULL,
    total_amount_minor bigint DEFAULT 0 NOT NULL,
    currency_code text NOT NULL,
    duration_minutes integer DEFAULT 0 NOT NULL,
    notes text DEFAULT ''::text NOT NULL,
    location_label text DEFAULT ''::text NOT NULL,
    image_url text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    promotion_id uuid,
    discount_name text DEFAULT ''::text NOT NULL,
    discount_source text DEFAULT ''::text NOT NULL,
    discount_code text DEFAULT ''::text NOT NULL,
    discount_type text DEFAULT ''::text NOT NULL,
    discount_amount_minor bigint DEFAULT 0 NOT NULL,
    original_amount_minor bigint DEFAULT 0 NOT NULL,
    deposit_amount_minor bigint DEFAULT 0 NOT NULL,
    country_code text NOT NULL,
    discount_percentage_bps bigint DEFAULT 0 NOT NULL,
    discount_value_minor bigint DEFAULT 0 NOT NULL,
    public_token text DEFAULT (replace((gen_random_uuid())::text, '-'::text, ''::text) || replace((gen_random_uuid())::text, '-'::text, ''::text)) NOT NULL,
    booking_quote_id uuid,
    discounted_service_amount_minor bigint DEFAULT 0 NOT NULL,
    short_notice_rule_id uuid,
    short_notice_threshold_minutes integer,
    short_notice_surcharge_type text DEFAULT ''::text NOT NULL,
    short_notice_surcharge_amount_minor bigint DEFAULT 0 NOT NULL,
    short_notice_surcharge_percentage_bps integer DEFAULT 0 NOT NULL,
    short_notice_fee_minor bigint DEFAULT 0 NOT NULL,
    travel_fee_minor bigint DEFAULT 0 NOT NULL,
    fulfillment_mode text DEFAULT 'provider_location'::text NOT NULL,
    provider_location_label text DEFAULT ''::text NOT NULL,
    provider_place_id text,
    provider_latitude numeric(9,6),
    provider_longitude numeric(9,6),
    customer_location_label text DEFAULT ''::text NOT NULL,
    customer_place_id text,
    customer_latitude numeric(9,6),
    customer_longitude numeric(9,6),
    travel_distance_meters integer,
    prep_time_minutes integer DEFAULT 0 NOT NULL,
    buffer_time_minutes integer DEFAULT 0 NOT NULL,
    occupied_start_at timestamp with time zone NOT NULL,
    occupied_end_at timestamp with time zone NOT NULL,
    virtual_delivery_label text DEFAULT ''::text NOT NULL,
    virtual_join_url text,
    virtual_instructions text,
    cancellation_policy_snapshot text DEFAULT ''::text NOT NULL,
    lateness_policy_snapshot text DEFAULT ''::text NOT NULL,
    agreement_template_family_id_snapshot uuid,
    agreement_template_version_id_snapshot uuid,
    agreement_title_snapshot text DEFAULT ''::text NOT NULL,
    agreement_booking_summary_snapshot jsonb DEFAULT '{}'::jsonb NOT NULL,
    agreement_resolved_document_snapshot jsonb,
    agreement_schema_version_snapshot integer,
    agreement_renderer_version_snapshot integer,
    agreement_rendered_html_snapshot text DEFAULT ''::text NOT NULL,
    agreement_resolved_terms_hash_snapshot text DEFAULT ''::text NOT NULL,
    agreement_confirmation_method_snapshot text DEFAULT ''::text NOT NULL,
    agreement_timing_snapshot text DEFAULT ''::text NOT NULL,
    standalone_signature_required_snapshot boolean DEFAULT false NOT NULL,
    CONSTRAINT bookings_country_code_check CHECK ((country_code ~ '^[A-Z]{2}$'::text)),
    CONSTRAINT bookings_currency_code_check CHECK ((currency_code ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT bookings_customer_location_check CHECK (((fulfillment_mode = 'customer_location'::text) OR ((customer_location_label = ''::text) AND (customer_place_id IS NULL) AND (customer_latitude IS NULL) AND (customer_longitude IS NULL)))),
    CONSTRAINT bookings_discount_value_shape_check CHECK ((((discount_type = ''::text) AND (discount_percentage_bps = 0) AND (discount_value_minor = 0)) OR ((discount_type = 'percentage'::text) AND ((discount_percentage_bps >= 1) AND (discount_percentage_bps <= 10000)) AND (discount_value_minor = 0)) OR ((discount_type = ANY (ARRAY['fixed_amount'::text, 'set_price'::text])) AND (discount_percentage_bps = 0) AND (discount_value_minor > 0)))),
    CONSTRAINT bookings_fulfillment_mode_check CHECK ((fulfillment_mode = ANY (ARRAY['provider_location'::text, 'customer_location'::text, 'virtual'::text]))),
    CONSTRAINT bookings_occupied_time_check CHECK (((occupied_start_at <= start_at) AND (start_at < end_at) AND (end_at <= occupied_end_at))),
    CONSTRAINT bookings_pricing_nonnegative_check CHECK (((base_service_amount_minor >= 0) AND (discounted_service_amount_minor >= 0) AND (short_notice_fee_minor >= 0) AND (travel_fee_minor >= 0) AND (total_amount_minor >= 0) AND (deposit_amount_minor >= 0))),
    CONSTRAINT bookings_pricing_total_check CHECK (((((discounted_service_amount_minor + short_notice_fee_minor) + travel_fee_minor) = total_amount_minor) AND (deposit_amount_minor <= total_amount_minor))),
    CONSTRAINT bookings_public_token_shape_check CHECK ((public_token ~ '^[A-Za-z0-9_-]{32,128}$'::text)),
    CONSTRAINT bookings_virtual_physical_check CHECK (((fulfillment_mode <> 'virtual'::text) OR ((provider_place_id IS NULL) AND (provider_latitude IS NULL) AND (provider_longitude IS NULL) AND (customer_place_id IS NULL) AND (customer_latitude IS NULL) AND (customer_longitude IS NULL))))
);


--
-- Name: business_balance_entries; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.business_balance_entries (
    id uuid NOT NULL,
    client_id uuid NOT NULL,
    payment_adjustment_id uuid,
    currency_code text NOT NULL,
    amount_minor bigint NOT NULL,
    kind text NOT NULL,
    status text DEFAULT 'open'::text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    resolved_at timestamp with time zone,
    CONSTRAINT business_balance_entries_amount_nonzero_check CHECK ((amount_minor <> 0)),
    CONSTRAINT business_balance_entries_currency_code_check CHECK ((currency_code ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT business_balance_entries_kind_check CHECK ((kind = ANY (ARRAY['credit'::text, 'debt'::text, 'recovery'::text, 'manual_adjustment'::text]))),
    CONSTRAINT business_balance_entries_status_check CHECK ((status = ANY (ARRAY['open'::text, 'partially_resolved'::text, 'resolved'::text, 'void'::text])))
);


--
-- Name: business_locations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.business_locations (
    id uuid NOT NULL,
    client_id uuid NOT NULL,
    label text NOT NULL,
    formatted_address text NOT NULL,
    provider_place_id text,
    latitude numeric(9,6),
    longitude numeric(9,6),
    address_source text NOT NULL,
    resolution_status text NOT NULL,
    timezone text NOT NULL,
    is_primary boolean DEFAULT false NOT NULL,
    is_active boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT business_locations_address_source_check CHECK ((address_source = ANY (ARRAY['manual'::text, 'google_place'::text, 'current_location'::text]))),
    CONSTRAINT business_locations_coordinate_pair_check CHECK ((((latitude IS NULL) AND (longitude IS NULL)) OR ((latitude IS NOT NULL) AND (longitude IS NOT NULL)))),
    CONSTRAINT business_locations_coordinate_range_check CHECK ((((latitude IS NULL) OR ((latitude >= ('-90'::integer)::numeric) AND (latitude <= (90)::numeric))) AND ((longitude IS NULL) OR ((longitude >= ('-180'::integer)::numeric) AND (longitude <= (180)::numeric))))),
    CONSTRAINT business_locations_required_text_check CHECK (((NULLIF(btrim(label), ''::text) IS NOT NULL) AND (NULLIF(btrim(formatted_address), ''::text) IS NOT NULL) AND (NULLIF(btrim(timezone), ''::text) IS NOT NULL))),
    CONSTRAINT business_locations_resolution_coordinate_check CHECK ((((resolution_status = 'text_only'::text) AND (latitude IS NULL) AND (longitude IS NULL)) OR ((resolution_status = 'coordinates_resolved'::text) AND (latitude IS NOT NULL) AND (longitude IS NOT NULL)))),
    CONSTRAINT business_locations_resolution_status_check CHECK ((resolution_status = ANY (ARRAY['text_only'::text, 'coordinates_resolved'::text])))
);


--
-- Name: client_profile_handles; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.client_profile_handles (
    handle_slug text NOT NULL,
    client_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: client_profiles; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.client_profiles (
    client_id uuid NOT NULL,
    business_name text DEFAULT ''::text NOT NULL,
    handle_slug text NOT NULL,
    category text DEFAULT ''::text NOT NULL,
    headline text DEFAULT ''::text NOT NULL,
    short_bio text DEFAULT ''::text NOT NULL,
    public_location_label text DEFAULT ''::text NOT NULL,
    city text DEFAULT ''::text NOT NULL,
    region text DEFAULT ''::text NOT NULL,
    timezone text,
    hero_image_url text,
    avatar_url text,
    verified boolean DEFAULT false NOT NULL,
    years_experience integer DEFAULT 0 NOT NULL,
    review_rating numeric(3,2) DEFAULT 0 NOT NULL,
    review_count integer DEFAULT 0 NOT NULL,
    currency_code text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    public_profile_about text DEFAULT ''::text NOT NULL,
    booking_page_intro text DEFAULT ''::text NOT NULL,
    country_code text,
    locale text,
    market_configured_at timestamp with time zone,
    CONSTRAINT client_profiles_market_tuple_check CHECK ((((country_code IS NULL) AND (currency_code IS NULL) AND (timezone IS NULL) AND (locale IS NULL) AND (market_configured_at IS NULL)) OR ((country_code ~ '^[A-Z]{2}$'::text) AND (currency_code ~ '^[A-Z]{3}$'::text) AND (NULLIF(btrim(timezone), ''::text) IS NOT NULL) AND (locale ~ '^[A-Za-z]{2,3}(-[A-Za-z0-9]{2,8})+$'::text) AND (market_configured_at IS NOT NULL))))
);


--
-- Name: clients; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.clients (
    id uuid NOT NULL,
    full_name text NOT NULL,
    email text NOT NULL,
    password_hash text NOT NULL,
    email_verified_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    bio text DEFAULT ''::text NOT NULL,
    cover_image_url text
);


--
-- Name: customers; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.customers (
    id uuid NOT NULL,
    client_id uuid NOT NULL,
    full_name text NOT NULL,
    email text DEFAULT ''::text NOT NULL,
    phone text DEFAULT ''::text NOT NULL,
    avatar_url text,
    tier_label text DEFAULT ''::text NOT NULL,
    status_label text DEFAULT ''::text NOT NULL,
    badge_label text DEFAULT ''::text NOT NULL,
    badge_tone text DEFAULT ''::text NOT NULL,
    tags text[] DEFAULT ARRAY[]::text[] NOT NULL,
    private_notes text DEFAULT ''::text NOT NULL,
    last_seen_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: financial_jobs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.financial_jobs (
    id uuid NOT NULL,
    kind text NOT NULL,
    aggregate_type text NOT NULL,
    aggregate_id uuid NOT NULL,
    deduplication_key text NOT NULL,
    payload jsonb NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    attempts integer DEFAULT 0 NOT NULL,
    available_at timestamp with time zone DEFAULT now() NOT NULL,
    lease_owner text DEFAULT ''::text NOT NULL,
    lease_expires_at timestamp with time zone,
    last_error text DEFAULT ''::text NOT NULL,
    completed_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT financial_jobs_attempts_check CHECK ((attempts >= 0)),
    CONSTRAINT financial_jobs_lease_check CHECK (((status <> 'processing'::text) OR ((NULLIF(btrim(lease_owner), ''::text) IS NOT NULL) AND (lease_expires_at IS NOT NULL)))),
    CONSTRAINT financial_jobs_required_text_check CHECK (((NULLIF(btrim(kind), ''::text) IS NOT NULL) AND (NULLIF(btrim(aggregate_type), ''::text) IS NOT NULL) AND (NULLIF(btrim(deduplication_key), ''::text) IS NOT NULL))),
    CONSTRAINT financial_jobs_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'processing'::text, 'completed'::text, 'failed'::text, 'cancelled'::text])))
);


--
-- Name: inbox_conversations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.inbox_conversations (
    id uuid NOT NULL,
    client_id uuid NOT NULL,
    customer_id uuid,
    source text DEFAULT ''::text NOT NULL,
    status text DEFAULT ''::text NOT NULL,
    subject text DEFAULT ''::text NOT NULL,
    preview text DEFAULT ''::text NOT NULL,
    avatar_url text,
    last_message_at timestamp with time zone DEFAULT now() NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    lead_name text DEFAULT ''::text NOT NULL,
    lead_contact text DEFAULT ''::text NOT NULL,
    external_lead_id text DEFAULT ''::text NOT NULL,
    autopilot_mode text DEFAULT 'manual'::text NOT NULL,
    agent_state text DEFAULT 'new_lead'::text NOT NULL,
    human_takeover boolean DEFAULT false NOT NULL,
    last_ai_reply_at timestamp with time zone,
    human_composing boolean DEFAULT false NOT NULL,
    human_composing_started_at timestamp with time zone,
    human_composing_expires_at timestamp with time zone
);


--
-- Name: inbox_messages; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.inbox_messages (
    id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    sender_role text DEFAULT ''::text NOT NULL,
    content text DEFAULT ''::text NOT NULL,
    message_type text DEFAULT 'text'::text NOT NULL,
    sent_at timestamp with time zone DEFAULT now() NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    action_type text DEFAULT ''::text NOT NULL
);


--
-- Name: notifications; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.notifications (
    id uuid NOT NULL,
    client_id uuid NOT NULL,
    customer_id uuid,
    booking_id uuid,
    type text DEFAULT ''::text NOT NULL,
    severity text DEFAULT ''::text NOT NULL,
    title text DEFAULT ''::text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    action_label text DEFAULT ''::text NOT NULL,
    action_route text DEFAULT ''::text NOT NULL,
    image_url text,
    icon_name text DEFAULT ''::text NOT NULL,
    icon_tone text DEFAULT ''::text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    read_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: payment_adjustments; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.payment_adjustments (
    id uuid NOT NULL,
    payment_id uuid NOT NULL,
    provider text NOT NULL,
    provider_reference text NOT NULL,
    kind text NOT NULL,
    status text NOT NULL,
    currency_code text NOT NULL,
    amount_minor bigint NOT NULL,
    reason text DEFAULT ''::text NOT NULL,
    occurred_at timestamp with time zone NOT NULL,
    allocation_impact_minor bigint DEFAULT 0 NOT NULL,
    funds_already_paid_out boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT payment_adjustments_allocation_impact_check CHECK ((allocation_impact_minor >= 0)),
    CONSTRAINT payment_adjustments_amount_positive_check CHECK ((amount_minor > 0)),
    CONSTRAINT payment_adjustments_currency_code_check CHECK ((currency_code ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT payment_adjustments_kind_check CHECK ((kind = ANY (ARRAY['partial_refund'::text, 'refund'::text, 'reversal'::text, 'dispute'::text, 'chargeback'::text]))),
    CONSTRAINT payment_adjustments_provider_check CHECK ((provider = ANY (ARRAY['payaza'::text, 'paystack'::text]))),
    CONSTRAINT payment_adjustments_reference_check CHECK ((NULLIF(btrim(provider_reference), ''::text) IS NOT NULL)),
    CONSTRAINT payment_adjustments_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'successful'::text, 'failed'::text, 'reversed'::text])))
);


--
-- Name: payment_allocations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.payment_allocations (
    id uuid NOT NULL,
    payment_id uuid NOT NULL,
    client_id uuid NOT NULL,
    currency_code text NOT NULL,
    gross_amount_minor bigint NOT NULL,
    provider_collection_fee_minor bigint DEFAULT 0 NOT NULL,
    platform_fee_minor bigint DEFAULT 0 NOT NULL,
    tax_amount_minor bigint DEFAULT 0 NOT NULL,
    adjustment_amount_minor bigint DEFAULT 0 NOT NULL,
    business_net_amount_minor bigint NOT NULL,
    policy_version text NOT NULL,
    calculation_snapshot jsonb NOT NULL,
    settlement_status text DEFAULT 'pending'::text NOT NULL,
    settlement_reference text DEFAULT ''::text NOT NULL,
    available_for_payout_at timestamp with time zone,
    status text DEFAULT 'pending'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT payment_allocations_amounts_check CHECK (((gross_amount_minor > 0) AND (provider_collection_fee_minor >= 0) AND (platform_fee_minor >= 0) AND (tax_amount_minor >= 0) AND (adjustment_amount_minor >= 0) AND (business_net_amount_minor >= 0) AND (gross_amount_minor = ((((provider_collection_fee_minor + platform_fee_minor) + tax_amount_minor) + adjustment_amount_minor) + business_net_amount_minor)))),
    CONSTRAINT payment_allocations_currency_code_check CHECK ((currency_code ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT payment_allocations_eligibility_check CHECK (((status <> 'eligible'::text) OR ((settlement_status = 'available'::text) AND (available_for_payout_at IS NOT NULL)))),
    CONSTRAINT payment_allocations_policy_version_check CHECK ((NULLIF(btrim(policy_version), ''::text) IS NOT NULL)),
    CONSTRAINT payment_allocations_settlement_status_check CHECK ((settlement_status = ANY (ARRAY['pending'::text, 'available'::text, 'held'::text, 'unavailable'::text, 'reversed'::text]))),
    CONSTRAINT payment_allocations_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'eligible'::text, 'reserved'::text, 'paid'::text, 'blocked'::text, 'reversed'::text])))
);


--
-- Name: payment_exceptions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.payment_exceptions (
    id uuid NOT NULL,
    payment_id uuid NOT NULL,
    booking_id uuid NOT NULL,
    provider text NOT NULL,
    exception_kind text NOT NULL,
    provider_reference text NOT NULL,
    evidence_source text NOT NULL,
    evidence_reference text NOT NULL,
    observed_amount_minor bigint NOT NULL,
    currency_code text NOT NULL,
    status text DEFAULT 'open'::text NOT NULL,
    resolution_notes text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    resolved_at timestamp with time zone,
    CONSTRAINT payment_exceptions_amount_check CHECK ((observed_amount_minor >= 0)),
    CONSTRAINT payment_exceptions_currency_check CHECK ((currency_code ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT payment_exceptions_kind_check CHECK ((exception_kind = ANY (ARRAY['late_success'::text, 'amount_mismatch'::text]))),
    CONSTRAINT payment_exceptions_provider_check CHECK ((provider = ANY (ARRAY['payaza'::text, 'paystack'::text]))),
    CONSTRAINT payment_exceptions_required_text_check CHECK (((NULLIF(btrim(provider_reference), ''::text) IS NOT NULL) AND (NULLIF(btrim(evidence_source), ''::text) IS NOT NULL) AND (NULLIF(btrim(evidence_reference), ''::text) IS NOT NULL))),
    CONSTRAINT payment_exceptions_resolution_check CHECK ((((status = 'open'::text) AND (resolved_at IS NULL)) OR ((status = 'resolved'::text) AND (resolved_at IS NOT NULL)))),
    CONSTRAINT payment_exceptions_status_check CHECK ((status = ANY (ARRAY['open'::text, 'resolved'::text])))
);


--
-- Name: payments; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.payments (
    id uuid NOT NULL,
    public_token text NOT NULL,
    booking_id uuid NOT NULL,
    client_id uuid NOT NULL,
    customer_id uuid NOT NULL,
    purpose text NOT NULL,
    provider text NOT NULL,
    method text NOT NULL,
    country_code text NOT NULL,
    currency_code text NOT NULL,
    amount_minor bigint NOT NULL,
    price_snapshot jsonb NOT NULL,
    reference text NOT NULL,
    provider_reference text DEFAULT ''::text NOT NULL,
    idempotency_key text NOT NULL,
    request_fingerprint text NOT NULL,
    status text NOT NULL,
    provider_status text DEFAULT ''::text NOT NULL,
    reconciliation_reason text DEFAULT ''::text NOT NULL,
    failure_code text DEFAULT ''::text NOT NULL,
    failure_message text DEFAULT ''::text NOT NULL,
    checkout_url text DEFAULT ''::text NOT NULL,
    expires_at timestamp with time zone,
    paid_at timestamp with time zone,
    last_reconciled_at timestamp with time zone,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    checkout_details jsonb DEFAULT '{}'::jsonb NOT NULL,
    reconciliation_lease_owner text DEFAULT ''::text NOT NULL,
    reconciliation_lease_expires_at timestamp with time zone,
    provider_channel text,
    checkout_initialization_state text DEFAULT 'ready'::text NOT NULL,
    checkout_initialization_lease_owner text DEFAULT ''::text NOT NULL,
    checkout_initialization_lease_expires_at timestamp with time zone,
    next_provider_check_at timestamp with time zone,
    CONSTRAINT payments_amount_positive_check CHECK ((amount_minor > 0)),
    CONSTRAINT payments_checkout_details_object_check CHECK ((jsonb_typeof(checkout_details) = 'object'::text)),
    CONSTRAINT payments_checkout_initialization_lease_check CHECK ((((checkout_initialization_lease_owner = ''::text) AND (checkout_initialization_lease_expires_at IS NULL)) OR ((NULLIF(btrim(checkout_initialization_lease_owner), ''::text) IS NOT NULL) AND (checkout_initialization_lease_expires_at IS NOT NULL)))),
    CONSTRAINT payments_checkout_initialization_state_check CHECK ((checkout_initialization_state = ANY (ARRAY['prepared'::text, 'ready'::text, 'unknown'::text]))),
    CONSTRAINT payments_country_code_check CHECK ((country_code ~ '^[A-Z]{2}$'::text)),
    CONSTRAINT payments_currency_code_check CHECK ((currency_code ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT payments_idempotency_key_shape_check CHECK ((idempotency_key ~ '^[A-Za-z0-9_-]{16,128}$'::text)),
    CONSTRAINT payments_paid_at_check CHECK (((status <> 'paid'::text) OR (paid_at IS NOT NULL))),
    CONSTRAINT payments_provider_channel_shape_check CHECK (((provider_channel IS NULL) OR (provider_channel ~ '^[a-z][a-z0-9_]{1,63}$'::text))),
    CONSTRAINT payments_provider_check CHECK ((provider = ANY (ARRAY['payaza'::text, 'paystack'::text]))),
    CONSTRAINT payments_public_token_shape_check CHECK ((public_token ~ '^[A-Za-z0-9_-]{32,128}$'::text)),
    CONSTRAINT payments_purpose_check CHECK ((purpose = ANY (ARRAY['deposit'::text, 'full'::text, 'balance'::text]))),
    CONSTRAINT payments_reconciliation_lease_check CHECK ((((reconciliation_lease_owner = ''::text) AND (reconciliation_lease_expires_at IS NULL)) OR ((NULLIF(btrim(reconciliation_lease_owner), ''::text) IS NOT NULL) AND (reconciliation_lease_expires_at IS NOT NULL)))),
    CONSTRAINT payments_reference_shape_check CHECK ((NULLIF(btrim(reference), ''::text) IS NOT NULL)),
    CONSTRAINT payments_request_fingerprint_shape_check CHECK ((request_fingerprint ~ '^[a-f0-9]{64}$'::text)),
    CONSTRAINT payments_status_check CHECK ((status = ANY (ARRAY['created'::text, 'pending'::text, 'requires_action'::text, 'paid'::text, 'partially_refunded'::text, 'refunded'::text, 'disputed'::text, 'reversed'::text, 'failed'::text, 'expired'::text, 'cancelled'::text]))),
    CONSTRAINT payments_version_positive_check CHECK ((version > 0))
);


--
-- Name: payout_destinations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.payout_destinations (
    id uuid NOT NULL,
    client_id uuid NOT NULL,
    provider text NOT NULL,
    country_code text NOT NULL,
    currency_code text NOT NULL,
    rail text NOT NULL,
    institution_code text NOT NULL,
    institution_name text NOT NULL,
    masked_identifier text NOT NULL,
    identifier_ciphertext bytea,
    identifier_nonce bytea,
    encryption_key_version text,
    resolved_account_name text NOT NULL,
    provider_recipient_id text DEFAULT ''::text NOT NULL,
    verification_fingerprint text NOT NULL,
    verified_at timestamp with time zone NOT NULL,
    is_default boolean DEFAULT false NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT payout_destinations_active_identifier_check CHECK (((status <> 'active'::text) OR (identifier_ciphertext IS NOT NULL))),
    CONSTRAINT payout_destinations_ciphertext_group_check CHECK ((((identifier_ciphertext IS NULL) AND (identifier_nonce IS NULL) AND (encryption_key_version IS NULL)) OR ((identifier_ciphertext IS NOT NULL) AND (identifier_nonce IS NOT NULL) AND (NULLIF(btrim(encryption_key_version), ''::text) IS NOT NULL)))),
    CONSTRAINT payout_destinations_country_code_check CHECK ((country_code ~ '^[A-Z]{2}$'::text)),
    CONSTRAINT payout_destinations_currency_code_check CHECK ((currency_code ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT payout_destinations_fingerprint_shape_check CHECK ((verification_fingerprint ~ '^[a-f0-9]{64}$'::text)),
    CONSTRAINT payout_destinations_provider_check CHECK ((provider = ANY (ARRAY['payaza'::text, 'paystack'::text]))),
    CONSTRAINT payout_destinations_required_text_check CHECK (((NULLIF(btrim(rail), ''::text) IS NOT NULL) AND (NULLIF(btrim(institution_code), ''::text) IS NOT NULL) AND (NULLIF(btrim(institution_name), ''::text) IS NOT NULL) AND (NULLIF(btrim(masked_identifier), ''::text) IS NOT NULL) AND (NULLIF(btrim(resolved_account_name), ''::text) IS NOT NULL))),
    CONSTRAINT payout_destinations_status_check CHECK ((status = ANY (ARRAY['active'::text, 'invalidated'::text, 'disabled'::text])))
);


--
-- Name: payouts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.payouts (
    id uuid NOT NULL,
    payment_allocation_id uuid NOT NULL,
    client_id uuid NOT NULL,
    payout_destination_id uuid NOT NULL,
    provider text NOT NULL,
    rail text NOT NULL,
    country_code text NOT NULL,
    currency_code text NOT NULL,
    amount_minor bigint NOT NULL,
    fee_minor bigint DEFAULT 0 NOT NULL,
    reference text NOT NULL,
    provider_reference text DEFAULT ''::text NOT NULL,
    idempotency_key text NOT NULL,
    request_fingerprint text NOT NULL,
    destination_snapshot jsonb NOT NULL,
    status text NOT NULL,
    provider_status text DEFAULT ''::text NOT NULL,
    failure_code text DEFAULT ''::text NOT NULL,
    failure_message text DEFAULT ''::text NOT NULL,
    initiated_at timestamp with time zone,
    completed_at timestamp with time zone,
    reversed_at timestamp with time zone,
    last_reconciled_at timestamp with time zone,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    reconciliation_lease_owner text DEFAULT ''::text NOT NULL,
    reconciliation_lease_expires_at timestamp with time zone,
    CONSTRAINT payouts_amount_positive_check CHECK ((amount_minor > 0)),
    CONSTRAINT payouts_country_code_check CHECK ((country_code ~ '^[A-Z]{2}$'::text)),
    CONSTRAINT payouts_currency_code_check CHECK ((currency_code ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT payouts_fee_nonnegative_check CHECK ((fee_minor >= 0)),
    CONSTRAINT payouts_idempotency_key_shape_check CHECK ((idempotency_key ~ '^[A-Za-z0-9_-]{16,128}$'::text)),
    CONSTRAINT payouts_provider_check CHECK ((provider = ANY (ARRAY['payaza'::text, 'paystack'::text]))),
    CONSTRAINT payouts_reconciliation_lease_check CHECK ((((reconciliation_lease_owner = ''::text) AND (reconciliation_lease_expires_at IS NULL)) OR ((NULLIF(btrim(reconciliation_lease_owner), ''::text) IS NOT NULL) AND (reconciliation_lease_expires_at IS NOT NULL)))),
    CONSTRAINT payouts_request_fingerprint_shape_check CHECK ((request_fingerprint ~ '^[a-f0-9]{64}$'::text)),
    CONSTRAINT payouts_required_text_check CHECK (((NULLIF(btrim(rail), ''::text) IS NOT NULL) AND (NULLIF(btrim(reference), ''::text) IS NOT NULL))),
    CONSTRAINT payouts_status_check CHECK ((status = ANY (ARRAY['created'::text, 'pending'::text, 'requires_action'::text, 'successful'::text, 'failed'::text, 'reversed'::text, 'cancelled'::text, 'unknown'::text]))),
    CONSTRAINT payouts_version_positive_check CHECK ((version > 0))
);


--
-- Name: promotion_redemptions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.promotion_redemptions (
    id uuid NOT NULL,
    promotion_id uuid NOT NULL,
    client_id uuid NOT NULL,
    booking_id uuid,
    customer_id uuid,
    customer_email text DEFAULT ''::text NOT NULL,
    code_used text DEFAULT ''::text NOT NULL,
    discount_amount_minor bigint DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    currency_code text NOT NULL,
    CONSTRAINT promotion_redemptions_currency_code_check CHECK ((currency_code ~ '^[A-Z]{3}$'::text))
);


--
-- Name: promotion_sections; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.promotion_sections (
    promotion_id uuid NOT NULL,
    section_id uuid NOT NULL
);


--
-- Name: promotion_services; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.promotion_services (
    promotion_id uuid NOT NULL,
    service_id uuid NOT NULL
);


--
-- Name: promotions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.promotions (
    id uuid NOT NULL,
    client_id uuid NOT NULL,
    name text NOT NULL,
    promotion_type text NOT NULL,
    code text DEFAULT ''::text NOT NULL,
    discount_type text NOT NULL,
    scope_type text DEFAULT 'all_services'::text NOT NULL,
    starts_at timestamp with time zone NOT NULL,
    ends_at timestamp with time zone,
    is_active boolean DEFAULT true NOT NULL,
    max_redemptions integer DEFAULT 0 NOT NULL,
    max_redemptions_per_customer integer DEFAULT 0 NOT NULL,
    minimum_spend_minor bigint DEFAULT 0 NOT NULL,
    first_time_customers_only boolean DEFAULT false NOT NULL,
    applies_to_deposit boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    stack_with_automatic_discounts boolean DEFAULT false NOT NULL,
    currency_code text NOT NULL,
    discount_percentage_bps bigint DEFAULT 0 NOT NULL,
    discount_value_minor bigint DEFAULT 0 NOT NULL,
    CONSTRAINT promotions_currency_code_check CHECK ((currency_code ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT promotions_discount_value_shape_check CHECK ((((discount_type = 'percentage'::text) AND ((discount_percentage_bps >= 1) AND (discount_percentage_bps <= 10000)) AND (discount_value_minor = 0)) OR ((discount_type = ANY (ARRAY['fixed_amount'::text, 'set_price'::text])) AND (discount_percentage_bps = 0) AND (discount_value_minor > 0))))
);


--
-- Name: provider_availability_windows; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.provider_availability_windows (
    id uuid NOT NULL,
    client_id uuid NOT NULL,
    day_of_week smallint NOT NULL,
    start_time time without time zone NOT NULL,
    end_time time without time zone NOT NULL,
    slot_interval_minutes integer DEFAULT 30 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT provider_availability_windows_day_of_week_check CHECK (((day_of_week >= 0) AND (day_of_week <= 6)))
);


--
-- Name: provider_portfolio_items; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.provider_portfolio_items (
    id uuid NOT NULL,
    client_id uuid NOT NULL,
    service_id uuid,
    title text DEFAULT ''::text NOT NULL,
    image_url text NOT NULL,
    sort_order integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: provider_reviews; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.provider_reviews (
    id uuid NOT NULL,
    client_id uuid NOT NULL,
    customer_id uuid,
    author_name text DEFAULT ''::text NOT NULL,
    rating smallint NOT NULL,
    review_text text DEFAULT ''::text NOT NULL,
    image_url text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT provider_reviews_rating_check CHECK (((rating >= 1) AND (rating <= 5)))
);


--
-- Name: provider_settlement_evidence; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.provider_settlement_evidence (
    id uuid NOT NULL,
    provider text NOT NULL,
    settlement_reference text NOT NULL,
    payment_reference text NOT NULL,
    payment_id uuid,
    provider_status text NOT NULL,
    amount_minor bigint NOT NULL,
    currency_code text NOT NULL,
    status text NOT NULL,
    available_at timestamp with time zone NOT NULL,
    observed_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT provider_settlement_evidence_amount_check CHECK ((amount_minor > 0)),
    CONSTRAINT provider_settlement_evidence_currency_check CHECK ((currency_code ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT provider_settlement_evidence_provider_check CHECK ((provider = ANY (ARRAY['payaza'::text, 'paystack'::text]))),
    CONSTRAINT provider_settlement_evidence_reference_check CHECK (((NULLIF(btrim(settlement_reference), ''::text) IS NOT NULL) AND (NULLIF(btrim(payment_reference), ''::text) IS NOT NULL))),
    CONSTRAINT provider_settlement_evidence_status_check CHECK ((status = ANY (ARRAY['unmatched'::text, 'available'::text, 'mismatched'::text])))
);


--
-- Name: provider_settlement_sync_states; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.provider_settlement_sync_states (
    provider text NOT NULL,
    cursor_at timestamp with time zone NOT NULL,
    last_success_at timestamp with time zone,
    lease_owner text DEFAULT ''::text NOT NULL,
    lease_expires_at timestamp with time zone,
    last_error text DEFAULT ''::text NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT provider_settlement_sync_lease_check CHECK ((((lease_owner = ''::text) AND (lease_expires_at IS NULL)) OR ((NULLIF(btrim(lease_owner), ''::text) IS NOT NULL) AND (lease_expires_at IS NOT NULL)))),
    CONSTRAINT provider_settlement_sync_provider_check CHECK ((provider = ANY (ARRAY['payaza'::text, 'paystack'::text])))
);


--
-- Name: provider_webhook_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.provider_webhook_events (
    id uuid NOT NULL,
    provider text NOT NULL,
    provider_event_id text DEFAULT ''::text NOT NULL,
    body_sha256 bytea NOT NULL,
    event_type text NOT NULL,
    raw_body_ciphertext bytea NOT NULL,
    raw_body_nonce bytea NOT NULL,
    encryption_key_version text NOT NULL,
    normalized_event jsonb NOT NULL,
    processing_status text DEFAULT 'pending'::text NOT NULL,
    processing_attempts integer DEFAULT 0 NOT NULL,
    received_at timestamp with time zone DEFAULT now() NOT NULL,
    verified_at timestamp with time zone NOT NULL,
    processed_at timestamp with time zone,
    next_attempt_at timestamp with time zone DEFAULT now() NOT NULL,
    processing_result text DEFAULT ''::text NOT NULL,
    processing_error text DEFAULT ''::text NOT NULL,
    processing_lease_expires_at timestamp with time zone,
    CONSTRAINT provider_webhook_events_attempts_check CHECK ((processing_attempts >= 0)),
    CONSTRAINT provider_webhook_events_body_hash_length_check CHECK ((octet_length(body_sha256) = 32)),
    CONSTRAINT provider_webhook_events_processing_status_check CHECK ((processing_status = ANY (ARRAY['pending'::text, 'processing'::text, 'completed'::text, 'failed'::text]))),
    CONSTRAINT provider_webhook_events_provider_check CHECK ((provider = ANY (ARRAY['payaza'::text, 'paystack'::text]))),
    CONSTRAINT provider_webhook_events_required_text_check CHECK (((NULLIF(btrim(event_type), ''::text) IS NOT NULL) AND (NULLIF(btrim(encryption_key_version), ''::text) IS NOT NULL)))
);


--
-- Name: resolved_locations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.resolved_locations (
    id uuid NOT NULL,
    public_token text NOT NULL,
    provider text NOT NULL,
    provider_place_id text,
    formatted_address text NOT NULL,
    latitude numeric(9,6),
    longitude numeric(9,6),
    address_source text NOT NULL,
    resolution_status text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT resolved_locations_address_source_check CHECK ((address_source = ANY (ARRAY['manual'::text, 'google_place'::text, 'current_location'::text]))),
    CONSTRAINT resolved_locations_coordinate_pair_check CHECK ((((latitude IS NULL) AND (longitude IS NULL)) OR ((latitude IS NOT NULL) AND (longitude IS NOT NULL)))),
    CONSTRAINT resolved_locations_coordinate_range_check CHECK ((((latitude IS NULL) OR ((latitude >= ('-90'::integer)::numeric) AND (latitude <= (90)::numeric))) AND ((longitude IS NULL) OR ((longitude >= ('-180'::integer)::numeric) AND (longitude <= (180)::numeric))))),
    CONSTRAINT resolved_locations_provider_check CHECK ((provider = ANY (ARRAY['manual'::text, 'google'::text]))),
    CONSTRAINT resolved_locations_required_address_check CHECK ((NULLIF(btrim(formatted_address), ''::text) IS NOT NULL)),
    CONSTRAINT resolved_locations_resolution_coordinate_check CHECK ((((resolution_status = 'text_only'::text) AND (latitude IS NULL) AND (longitude IS NULL)) OR ((resolution_status = 'coordinates_resolved'::text) AND (latitude IS NOT NULL) AND (longitude IS NOT NULL)))),
    CONSTRAINT resolved_locations_resolution_status_check CHECK ((resolution_status = ANY (ARRAY['text_only'::text, 'coordinates_resolved'::text])))
);


--
-- Name: schema_migrations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.schema_migrations (
    version character varying NOT NULL
);


--
-- Name: service_availability_windows; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.service_availability_windows (
    id uuid NOT NULL,
    service_id uuid NOT NULL,
    day_of_week smallint NOT NULL,
    start_time time without time zone NOT NULL,
    end_time time without time zone NOT NULL,
    slot_interval_minutes integer NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT service_availability_windows_day_check CHECK (((day_of_week >= 0) AND (day_of_week <= 6))),
    CONSTRAINT service_availability_windows_interval_check CHECK ((slot_interval_minutes > 0)),
    CONSTRAINT service_availability_windows_time_check CHECK ((end_time > start_time))
);


--
-- Name: service_sections; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.service_sections (
    id uuid NOT NULL,
    client_id uuid NOT NULL,
    name text NOT NULL,
    slug text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    cover_image_url text,
    sort_order integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: service_short_notice_rules; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.service_short_notice_rules (
    id uuid NOT NULL,
    service_id uuid NOT NULL,
    threshold_minutes integer NOT NULL,
    surcharge_type text NOT NULL,
    surcharge_amount_minor bigint DEFAULT 0 NOT NULL,
    surcharge_percentage_bps integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT service_short_notice_rules_threshold_check CHECK ((threshold_minutes > 0)),
    CONSTRAINT service_short_notice_rules_type_check CHECK ((surcharge_type = ANY (ARRAY['fixed_amount'::text, 'percentage'::text]))),
    CONSTRAINT service_short_notice_rules_value_check CHECK ((((surcharge_type = 'fixed_amount'::text) AND (surcharge_amount_minor > 0) AND (surcharge_percentage_bps = 0)) OR ((surcharge_type = 'percentage'::text) AND (surcharge_amount_minor = 0) AND ((surcharge_percentage_bps >= 1) AND (surcharge_percentage_bps <= 10000)))))
);


--
-- Name: service_wizard_drafts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.service_wizard_drafts (
    id uuid NOT NULL,
    client_id uuid NOT NULL,
    service_id uuid,
    payload jsonb NOT NULL,
    current_step text NOT NULL,
    revision bigint DEFAULT 1 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT service_wizard_drafts_current_step_check CHECK ((current_step = ANY (ARRAY['choose-section'::text, 'info'::text, 'pricing'::text, 'duration'::text, 'availability'::text, 'location'::text, 'policy'::text, 'agreement-settings'::text, 'preview'::text]))),
    CONSTRAINT service_wizard_drafts_payload_object_check CHECK ((jsonb_typeof(payload) = 'object'::text)),
    CONSTRAINT service_wizard_drafts_revision_check CHECK ((revision > 0))
);


--
-- Name: services; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.services (
    id uuid NOT NULL,
    client_id uuid NOT NULL,
    title text NOT NULL,
    slug text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    category text DEFAULT ''::text NOT NULL,
    icon_name text DEFAULT ''::text NOT NULL,
    image_url text,
    duration_minutes integer DEFAULT 0 NOT NULL,
    price_amount_minor bigint DEFAULT 0 NOT NULL,
    is_active boolean DEFAULT true NOT NULL,
    sort_order integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    section_id uuid,
    status text DEFAULT 'published'::text NOT NULL,
    compare_price_amount_minor bigint DEFAULT 0 NOT NULL,
    deposit_required boolean DEFAULT false NOT NULL,
    deposit_type text DEFAULT 'fixed'::text NOT NULL,
    deposit_amount_minor bigint DEFAULT 0 NOT NULL,
    deposit_percentage_bps integer DEFAULT 0 NOT NULL,
    prep_time_minutes integer DEFAULT 0 NOT NULL,
    buffer_time_minutes integer DEFAULT 0 NOT NULL,
    availability_mode text DEFAULT 'inherit_business_hours'::text NOT NULL,
    minimum_notice_minutes integer DEFAULT 0 NOT NULL,
    max_bookings_per_day integer DEFAULT 0 NOT NULL,
    fulfillment_mode text DEFAULT 'provider_location'::text NOT NULL,
    travel_fee_minor bigint DEFAULT 0 NOT NULL,
    max_travel_distance_meters integer,
    cancellation_policy text DEFAULT ''::text NOT NULL,
    lateness_policy text DEFAULT ''::text NOT NULL,
    prep_aftercare_instructions text DEFAULT ''::text NOT NULL,
    badge text DEFAULT ''::text NOT NULL,
    agreement_timing text DEFAULT 'before_payment'::text,
    is_hidden boolean DEFAULT false NOT NULL,
    currency_code text NOT NULL,
    provider_location_id uuid,
    virtual_delivery_label text DEFAULT ''::text NOT NULL,
    virtual_join_url text,
    virtual_instructions text,
    agreement_template_family_id uuid,
    standalone_signature_required boolean DEFAULT false NOT NULL,
    CONSTRAINT services_agreement_configuration_check CHECK ((((agreement_template_family_id IS NULL) AND (agreement_timing IS NULL)) OR ((agreement_template_family_id IS NOT NULL) AND (agreement_timing = ANY (ARRAY['before_payment'::text, 'after_payment'::text])) AND (standalone_signature_required = false)))),
    CONSTRAINT services_availability_mode_check CHECK ((availability_mode = ANY (ARRAY['inherit_business_hours'::text, 'custom'::text]))),
    CONSTRAINT services_currency_code_check CHECK ((currency_code ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT services_deposit_percentage_bps_check CHECK (((deposit_percentage_bps >= 0) AND (deposit_percentage_bps <= 10000))),
    CONSTRAINT services_fulfillment_mode_check CHECK ((fulfillment_mode = ANY (ARRAY['provider_location'::text, 'customer_location'::text, 'virtual'::text]))),
    CONSTRAINT services_max_travel_distance_check CHECK (((max_travel_distance_meters IS NULL) OR ((fulfillment_mode = 'customer_location'::text) AND (max_travel_distance_meters > 0)))),
    CONSTRAINT services_nonnegative_scheduling_check CHECK (((minimum_notice_minutes >= 0) AND (prep_time_minutes >= 0) AND (buffer_time_minutes >= 0) AND (max_bookings_per_day >= 0))),
    CONSTRAINT services_published_location_check CHECK (((status <> 'published'::text) OR (fulfillment_mode = 'virtual'::text) OR (provider_location_id IS NOT NULL))),
    CONSTRAINT services_travel_fee_check CHECK (((travel_fee_minor >= 0) AND ((fulfillment_mode = 'customer_location'::text) OR (travel_fee_minor = 0)))),
    CONSTRAINT services_virtual_fields_check CHECK (((fulfillment_mode = 'virtual'::text) OR ((virtual_delivery_label = ''::text) AND (virtual_join_url IS NULL) AND (virtual_instructions IS NULL))))
);


--
-- Name: agreement_acceptances agreement_acceptances_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agreement_acceptances
    ADD CONSTRAINT agreement_acceptances_pkey PRIMARY KEY (agreement_id);


--
-- Name: agreement_events agreement_events_agreement_id_dedupe_key_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agreement_events
    ADD CONSTRAINT agreement_events_agreement_id_dedupe_key_key UNIQUE (agreement_id, dedupe_key);


--
-- Name: agreement_events agreement_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agreement_events
    ADD CONSTRAINT agreement_events_pkey PRIMARY KEY (id);


--
-- Name: agreement_instances agreement_instances_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agreement_instances
    ADD CONSTRAINT agreement_instances_pkey PRIMARY KEY (id);


--
-- Name: agreement_instances agreement_instances_public_token_hash_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agreement_instances
    ADD CONSTRAINT agreement_instances_public_token_hash_key UNIQUE (public_token_hash);


--
-- Name: agreement_jobs agreement_jobs_dedupe_key_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agreement_jobs
    ADD CONSTRAINT agreement_jobs_dedupe_key_key UNIQUE (dedupe_key);


--
-- Name: agreement_jobs agreement_jobs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agreement_jobs
    ADD CONSTRAINT agreement_jobs_pkey PRIMARY KEY (id);


--
-- Name: agreement_template_families agreement_template_families_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agreement_template_families
    ADD CONSTRAINT agreement_template_families_pkey PRIMARY KEY (id);


--
-- Name: agreement_template_generation_jobs agreement_template_generation_jobs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agreement_template_generation_jobs
    ADD CONSTRAINT agreement_template_generation_jobs_pkey PRIMARY KEY (id);


--
-- Name: agreement_template_versions agreement_template_versions_family_id_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agreement_template_versions
    ADD CONSTRAINT agreement_template_versions_family_id_id_key UNIQUE (family_id, id);


--
-- Name: agreement_template_versions agreement_template_versions_family_id_version_number_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agreement_template_versions
    ADD CONSTRAINT agreement_template_versions_family_id_version_number_key UNIQUE (family_id, version_number);


--
-- Name: agreement_template_versions agreement_template_versions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agreement_template_versions
    ADD CONSTRAINT agreement_template_versions_pkey PRIMARY KEY (id);


--
-- Name: auth_password_reset_tokens auth_password_reset_tokens_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.auth_password_reset_tokens
    ADD CONSTRAINT auth_password_reset_tokens_pkey PRIMARY KEY (id);


--
-- Name: auth_password_reset_tokens auth_password_reset_tokens_token_hash_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.auth_password_reset_tokens
    ADD CONSTRAINT auth_password_reset_tokens_token_hash_key UNIQUE (token_hash);


--
-- Name: auth_pending_registrations auth_pending_registrations_email_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.auth_pending_registrations
    ADD CONSTRAINT auth_pending_registrations_email_key UNIQUE (email);


--
-- Name: auth_pending_registrations auth_pending_registrations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.auth_pending_registrations
    ADD CONSTRAINT auth_pending_registrations_pkey PRIMARY KEY (id);


--
-- Name: auth_refresh_sessions auth_refresh_sessions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.auth_refresh_sessions
    ADD CONSTRAINT auth_refresh_sessions_pkey PRIMARY KEY (id);


--
-- Name: auth_refresh_sessions auth_refresh_sessions_token_hash_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.auth_refresh_sessions
    ADD CONSTRAINT auth_refresh_sessions_token_hash_key UNIQUE (token_hash);


--
-- Name: automation_settings automation_settings_client_id_automation_key_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.automation_settings
    ADD CONSTRAINT automation_settings_client_id_automation_key_key UNIQUE (client_id, automation_key);


--
-- Name: automation_settings automation_settings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.automation_settings
    ADD CONSTRAINT automation_settings_pkey PRIMARY KEY (id);


--
-- Name: booking_quote_promotions booking_quote_promotions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.booking_quote_promotions
    ADD CONSTRAINT booking_quote_promotions_pkey PRIMARY KEY (booking_quote_id, promotion_id);


--
-- Name: booking_quotes booking_quotes_booking_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.booking_quotes
    ADD CONSTRAINT booking_quotes_booking_id_key UNIQUE (booking_id);


--
-- Name: booking_quotes booking_quotes_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.booking_quotes
    ADD CONSTRAINT booking_quotes_pkey PRIMARY KEY (id);


--
-- Name: booking_quotes booking_quotes_public_token_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.booking_quotes
    ADD CONSTRAINT booking_quotes_public_token_key UNIQUE (public_token);


--
-- Name: booking_standalone_signatures booking_standalone_signatures_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.booking_standalone_signatures
    ADD CONSTRAINT booking_standalone_signatures_pkey PRIMARY KEY (booking_id);


--
-- Name: bookings bookings_booking_quote_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bookings
    ADD CONSTRAINT bookings_booking_quote_id_key UNIQUE (booking_quote_id);


--
-- Name: bookings bookings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bookings
    ADD CONSTRAINT bookings_pkey PRIMARY KEY (id);


--
-- Name: bookings bookings_public_token_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bookings
    ADD CONSTRAINT bookings_public_token_key UNIQUE (public_token);


--
-- Name: business_balance_entries business_balance_entries_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.business_balance_entries
    ADD CONSTRAINT business_balance_entries_pkey PRIMARY KEY (id);


--
-- Name: business_locations business_locations_client_id_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.business_locations
    ADD CONSTRAINT business_locations_client_id_id_key UNIQUE (client_id, id);


--
-- Name: business_locations business_locations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.business_locations
    ADD CONSTRAINT business_locations_pkey PRIMARY KEY (id);


--
-- Name: client_profile_handles client_profile_handles_client_id_handle_slug_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.client_profile_handles
    ADD CONSTRAINT client_profile_handles_client_id_handle_slug_key UNIQUE (client_id, handle_slug);


--
-- Name: client_profile_handles client_profile_handles_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.client_profile_handles
    ADD CONSTRAINT client_profile_handles_pkey PRIMARY KEY (handle_slug);


--
-- Name: client_profiles client_profiles_handle_slug_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.client_profiles
    ADD CONSTRAINT client_profiles_handle_slug_key UNIQUE (handle_slug);


--
-- Name: client_profiles client_profiles_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.client_profiles
    ADD CONSTRAINT client_profiles_pkey PRIMARY KEY (client_id);


--
-- Name: customers customers_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.customers
    ADD CONSTRAINT customers_pkey PRIMARY KEY (id);


--
-- Name: financial_jobs financial_jobs_deduplication_key_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.financial_jobs
    ADD CONSTRAINT financial_jobs_deduplication_key_key UNIQUE (deduplication_key);


--
-- Name: financial_jobs financial_jobs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.financial_jobs
    ADD CONSTRAINT financial_jobs_pkey PRIMARY KEY (id);


--
-- Name: inbox_conversations inbox_conversations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_conversations
    ADD CONSTRAINT inbox_conversations_pkey PRIMARY KEY (id);


--
-- Name: inbox_messages inbox_messages_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_messages
    ADD CONSTRAINT inbox_messages_pkey PRIMARY KEY (id);


--
-- Name: notifications notifications_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.notifications
    ADD CONSTRAINT notifications_pkey PRIMARY KEY (id);


--
-- Name: payment_adjustments payment_adjustments_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payment_adjustments
    ADD CONSTRAINT payment_adjustments_pkey PRIMARY KEY (id);


--
-- Name: payment_adjustments payment_adjustments_provider_reference_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payment_adjustments
    ADD CONSTRAINT payment_adjustments_provider_reference_key UNIQUE (provider, provider_reference);


--
-- Name: payment_allocations payment_allocations_payment_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payment_allocations
    ADD CONSTRAINT payment_allocations_payment_id_key UNIQUE (payment_id);


--
-- Name: payment_allocations payment_allocations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payment_allocations
    ADD CONSTRAINT payment_allocations_pkey PRIMARY KEY (id);


--
-- Name: payment_exceptions payment_exceptions_evidence_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payment_exceptions
    ADD CONSTRAINT payment_exceptions_evidence_key UNIQUE (provider, evidence_reference, exception_kind);


--
-- Name: payment_exceptions payment_exceptions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payment_exceptions
    ADD CONSTRAINT payment_exceptions_pkey PRIMARY KEY (id);


--
-- Name: payments payments_idempotency_scope_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payments
    ADD CONSTRAINT payments_idempotency_scope_key UNIQUE (booking_id, purpose, idempotency_key);


--
-- Name: payments payments_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payments
    ADD CONSTRAINT payments_pkey PRIMARY KEY (id);


--
-- Name: payments payments_public_token_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payments
    ADD CONSTRAINT payments_public_token_key UNIQUE (public_token);


--
-- Name: payments payments_reference_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payments
    ADD CONSTRAINT payments_reference_key UNIQUE (reference);


--
-- Name: payout_destinations payout_destinations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payout_destinations
    ADD CONSTRAINT payout_destinations_pkey PRIMARY KEY (id);


--
-- Name: payouts payouts_idempotency_scope_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payouts
    ADD CONSTRAINT payouts_idempotency_scope_key UNIQUE (client_id, idempotency_key);


--
-- Name: payouts payouts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payouts
    ADD CONSTRAINT payouts_pkey PRIMARY KEY (id);


--
-- Name: payouts payouts_reference_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payouts
    ADD CONSTRAINT payouts_reference_key UNIQUE (reference);


--
-- Name: promotion_redemptions promotion_redemptions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.promotion_redemptions
    ADD CONSTRAINT promotion_redemptions_pkey PRIMARY KEY (id);


--
-- Name: promotion_sections promotion_sections_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.promotion_sections
    ADD CONSTRAINT promotion_sections_pkey PRIMARY KEY (promotion_id, section_id);


--
-- Name: promotion_services promotion_services_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.promotion_services
    ADD CONSTRAINT promotion_services_pkey PRIMARY KEY (promotion_id, service_id);


--
-- Name: promotions promotions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.promotions
    ADD CONSTRAINT promotions_pkey PRIMARY KEY (id);


--
-- Name: provider_availability_windows provider_availability_windows_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_availability_windows
    ADD CONSTRAINT provider_availability_windows_pkey PRIMARY KEY (id);


--
-- Name: provider_portfolio_items provider_portfolio_items_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_portfolio_items
    ADD CONSTRAINT provider_portfolio_items_pkey PRIMARY KEY (id);


--
-- Name: provider_reviews provider_reviews_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_reviews
    ADD CONSTRAINT provider_reviews_pkey PRIMARY KEY (id);


--
-- Name: provider_settlement_evidence provider_settlement_evidence_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_settlement_evidence
    ADD CONSTRAINT provider_settlement_evidence_key UNIQUE (provider, settlement_reference, payment_reference);


--
-- Name: provider_settlement_evidence provider_settlement_evidence_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_settlement_evidence
    ADD CONSTRAINT provider_settlement_evidence_pkey PRIMARY KEY (id);


--
-- Name: provider_settlement_sync_states provider_settlement_sync_states_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_settlement_sync_states
    ADD CONSTRAINT provider_settlement_sync_states_pkey PRIMARY KEY (provider);


--
-- Name: provider_webhook_events provider_webhook_events_body_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_webhook_events
    ADD CONSTRAINT provider_webhook_events_body_key UNIQUE (provider, body_sha256);


--
-- Name: provider_webhook_events provider_webhook_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_webhook_events
    ADD CONSTRAINT provider_webhook_events_pkey PRIMARY KEY (id);


--
-- Name: resolved_locations resolved_locations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.resolved_locations
    ADD CONSTRAINT resolved_locations_pkey PRIMARY KEY (id);


--
-- Name: resolved_locations resolved_locations_public_token_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.resolved_locations
    ADD CONSTRAINT resolved_locations_public_token_key UNIQUE (public_token);


--
-- Name: schema_migrations schema_migrations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.schema_migrations
    ADD CONSTRAINT schema_migrations_pkey PRIMARY KEY (version);


--
-- Name: service_availability_windows service_availability_windows_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.service_availability_windows
    ADD CONSTRAINT service_availability_windows_pkey PRIMARY KEY (id);


--
-- Name: service_sections service_sections_client_id_slug_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.service_sections
    ADD CONSTRAINT service_sections_client_id_slug_key UNIQUE (client_id, slug);


--
-- Name: service_sections service_sections_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.service_sections
    ADD CONSTRAINT service_sections_pkey PRIMARY KEY (id);


--
-- Name: service_short_notice_rules service_short_notice_rules_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.service_short_notice_rules
    ADD CONSTRAINT service_short_notice_rules_pkey PRIMARY KEY (id);


--
-- Name: service_short_notice_rules service_short_notice_rules_service_threshold_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.service_short_notice_rules
    ADD CONSTRAINT service_short_notice_rules_service_threshold_key UNIQUE (service_id, threshold_minutes);


--
-- Name: service_wizard_drafts service_wizard_drafts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.service_wizard_drafts
    ADD CONSTRAINT service_wizard_drafts_pkey PRIMARY KEY (id);


--
-- Name: services services_client_id_slug_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.services
    ADD CONSTRAINT services_client_id_slug_key UNIQUE (client_id, slug);


--
-- Name: services services_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.services
    ADD CONSTRAINT services_pkey PRIMARY KEY (id);


--
-- Name: clients users_email_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.clients
    ADD CONSTRAINT users_email_key UNIQUE (email);


--
-- Name: clients users_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.clients
    ADD CONSTRAINT users_pkey PRIMARY KEY (id);


--
-- Name: agreement_events_agreement_occurred_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX agreement_events_agreement_occurred_idx ON public.agreement_events USING btree (agreement_id, occurred_at);


--
-- Name: agreement_instances_booking_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX agreement_instances_booking_idx ON public.agreement_instances USING btree (booking_id) WHERE (booking_id IS NOT NULL);


--
-- Name: agreement_instances_client_created_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX agreement_instances_client_created_idx ON public.agreement_instances USING btree (client_id, created_at DESC);


--
-- Name: agreement_instances_customer_created_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX agreement_instances_customer_created_idx ON public.agreement_instances USING btree (customer_id, created_at DESC) WHERE (customer_id IS NOT NULL);


--
-- Name: agreement_instances_one_automated_per_booking_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX agreement_instances_one_automated_per_booking_idx ON public.agreement_instances USING btree (booking_id) WHERE ((booking_id IS NOT NULL) AND (timing = ANY (ARRAY['before_payment'::text, 'after_payment'::text])));


--
-- Name: agreement_jobs_claim_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX agreement_jobs_claim_idx ON public.agreement_jobs USING btree (run_at, created_at) WHERE (status = ANY (ARRAY['queued'::text, 'processing'::text]));


--
-- Name: agreement_template_families_client_list_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX agreement_template_families_client_list_idx ON public.agreement_template_families USING btree (client_id, updated_at DESC, id DESC) WHERE (owner_type = 'client'::text);


--
-- Name: agreement_template_families_system_list_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX agreement_template_families_system_list_idx ON public.agreement_template_families USING btree (category, updated_at DESC, id DESC) WHERE (owner_type = 'system'::text);


--
-- Name: agreement_template_generation_jobs_claim_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX agreement_template_generation_jobs_claim_idx ON public.agreement_template_generation_jobs USING btree (run_at, created_at) WHERE (status = ANY (ARRAY['queued'::text, 'processing'::text]));


--
-- Name: agreement_template_generation_jobs_one_active_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX agreement_template_generation_jobs_one_active_idx ON public.agreement_template_generation_jobs USING btree (version_id) WHERE (status = ANY (ARRAY['queued'::text, 'processing'::text]));


--
-- Name: agreement_template_versions_family_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX agreement_template_versions_family_idx ON public.agreement_template_versions USING btree (family_id, version_number DESC);


--
-- Name: agreement_template_versions_one_draft_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX agreement_template_versions_one_draft_idx ON public.agreement_template_versions USING btree (family_id) WHERE (state = 'draft'::text);


--
-- Name: auth_password_reset_tokens_expires_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX auth_password_reset_tokens_expires_at_idx ON public.auth_password_reset_tokens USING btree (expires_at);


--
-- Name: auth_password_reset_tokens_user_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX auth_password_reset_tokens_user_id_idx ON public.auth_password_reset_tokens USING btree (client_id);


--
-- Name: auth_pending_registrations_expires_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX auth_pending_registrations_expires_at_idx ON public.auth_pending_registrations USING btree (expires_at);


--
-- Name: auth_refresh_sessions_expires_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX auth_refresh_sessions_expires_at_idx ON public.auth_refresh_sessions USING btree (expires_at);


--
-- Name: auth_refresh_sessions_user_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX auth_refresh_sessions_user_id_idx ON public.auth_refresh_sessions USING btree (client_id);


--
-- Name: automation_settings_client_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX automation_settings_client_id_idx ON public.automation_settings USING btree (client_id);


--
-- Name: booking_quote_promotions_capacity_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX booking_quote_promotions_capacity_idx ON public.booking_quote_promotions USING btree (promotion_id, customer_email_normalized, booking_quote_id);


--
-- Name: booking_quotes_customer_promotion_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX booking_quotes_customer_promotion_idx ON public.booking_quotes USING btree (promotion_id, customer_email_normalized, consumed_at, expires_at) WHERE (promotion_id IS NOT NULL);


--
-- Name: booking_quotes_promotion_reservation_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX booking_quotes_promotion_reservation_idx ON public.booking_quotes USING btree (promotion_id, consumed_at, expires_at) WHERE (promotion_id IS NOT NULL);


--
-- Name: booking_quotes_service_expiry_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX booking_quotes_service_expiry_idx ON public.booking_quotes USING btree (service_id, expires_at);


--
-- Name: bookings_client_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX bookings_client_id_idx ON public.bookings USING btree (client_id);


--
-- Name: bookings_client_id_start_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX bookings_client_id_start_at_idx ON public.bookings USING btree (client_id, start_at);


--
-- Name: bookings_customer_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX bookings_customer_id_idx ON public.bookings USING btree (customer_id);


--
-- Name: bookings_promotion_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX bookings_promotion_id_idx ON public.bookings USING btree (promotion_id);


--
-- Name: business_balance_entries_adjustment_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX business_balance_entries_adjustment_key ON public.business_balance_entries USING btree (payment_adjustment_id) WHERE (payment_adjustment_id IS NOT NULL);


--
-- Name: business_balance_entries_client_open_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX business_balance_entries_client_open_idx ON public.business_balance_entries USING btree (client_id, currency_code, created_at) WHERE (status = ANY (ARRAY['open'::text, 'partially_resolved'::text]));


--
-- Name: business_locations_client_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX business_locations_client_id_idx ON public.business_locations USING btree (client_id, is_active);


--
-- Name: business_locations_one_active_primary_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX business_locations_one_active_primary_idx ON public.business_locations USING btree (client_id) WHERE (is_primary AND is_active);


--
-- Name: client_profiles_country_code_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX client_profiles_country_code_idx ON public.client_profiles USING btree (country_code) WHERE (country_code IS NOT NULL);


--
-- Name: customers_client_id_full_name_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX customers_client_id_full_name_idx ON public.customers USING btree (client_id, full_name);


--
-- Name: customers_client_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX customers_client_id_idx ON public.customers USING btree (client_id);


--
-- Name: financial_jobs_claim_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX financial_jobs_claim_idx ON public.financial_jobs USING btree (available_at, created_at) WHERE (status = ANY (ARRAY['pending'::text, 'failed'::text]));


--
-- Name: idx_services_client_hidden_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_services_client_hidden_status ON public.services USING btree (client_id, is_hidden, status);


--
-- Name: inbox_conversations_client_id_external_lead_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX inbox_conversations_client_id_external_lead_id_idx ON public.inbox_conversations USING btree (client_id, external_lead_id, last_message_at DESC);


--
-- Name: inbox_conversations_client_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX inbox_conversations_client_id_idx ON public.inbox_conversations USING btree (client_id);


--
-- Name: inbox_conversations_client_id_last_message_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX inbox_conversations_client_id_last_message_at_idx ON public.inbox_conversations USING btree (client_id, last_message_at DESC);


--
-- Name: inbox_conversations_client_id_status_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX inbox_conversations_client_id_status_idx ON public.inbox_conversations USING btree (client_id, status);


--
-- Name: inbox_messages_conversation_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX inbox_messages_conversation_id_idx ON public.inbox_messages USING btree (conversation_id, sent_at);


--
-- Name: notifications_client_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX notifications_client_id_idx ON public.notifications USING btree (client_id, created_at DESC);


--
-- Name: notifications_client_id_read_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX notifications_client_id_read_at_idx ON public.notifications USING btree (client_id, read_at);


--
-- Name: payment_adjustments_payment_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX payment_adjustments_payment_idx ON public.payment_adjustments USING btree (payment_id, occurred_at DESC);


--
-- Name: payment_allocations_eligible_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX payment_allocations_eligible_idx ON public.payment_allocations USING btree (available_for_payout_at, created_at) WHERE (status = 'eligible'::text);


--
-- Name: payment_allocations_wallet_balance_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX payment_allocations_wallet_balance_idx ON public.payment_allocations USING btree (client_id, currency_code, available_for_payout_at) WHERE (status = 'eligible'::text);


--
-- Name: payment_exceptions_open_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX payment_exceptions_open_idx ON public.payment_exceptions USING btree (created_at) WHERE (status = 'open'::text);


--
-- Name: payments_booking_created_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX payments_booking_created_idx ON public.payments USING btree (booking_id, created_at DESC);


--
-- Name: payments_checkout_initialization_claim_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX payments_checkout_initialization_claim_idx ON public.payments USING btree (next_provider_check_at, checkout_initialization_lease_expires_at) WHERE ((checkout_initialization_state = ANY (ARRAY['prepared'::text, 'unknown'::text])) AND (status = ANY (ARRAY['created'::text, 'pending'::text, 'requires_action'::text])));


--
-- Name: payments_client_paid_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX payments_client_paid_at_idx ON public.payments USING btree (client_id, paid_at DESC) WHERE (paid_at IS NOT NULL);


--
-- Name: payments_one_active_obligation_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX payments_one_active_obligation_idx ON public.payments USING btree (booking_id, purpose) WHERE (status = ANY (ARRAY['created'::text, 'pending'::text, 'requires_action'::text]));


--
-- Name: payments_pending_reconciliation_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX payments_pending_reconciliation_idx ON public.payments USING btree (updated_at) WHERE (status = ANY (ARRAY['created'::text, 'pending'::text, 'requires_action'::text]));


--
-- Name: payments_provider_reference_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX payments_provider_reference_key ON public.payments USING btree (provider, provider_reference) WHERE (provider_reference <> ''::text);


--
-- Name: payments_reconciliation_claim_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX payments_reconciliation_claim_idx ON public.payments USING btree (updated_at) WHERE (status = ANY (ARRAY['created'::text, 'pending'::text, 'requires_action'::text]));


--
-- Name: payout_destinations_active_verification_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX payout_destinations_active_verification_key ON public.payout_destinations USING btree (client_id, provider, country_code, currency_code, rail, institution_code, verification_fingerprint) WHERE (status = 'active'::text);


--
-- Name: payout_destinations_client_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX payout_destinations_client_idx ON public.payout_destinations USING btree (client_id, status, created_at DESC);


--
-- Name: payout_destinations_one_default_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX payout_destinations_one_default_idx ON public.payout_destinations USING btree (client_id, country_code, currency_code, rail) WHERE (is_default AND (status = 'active'::text));


--
-- Name: payouts_one_active_allocation_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX payouts_one_active_allocation_idx ON public.payouts USING btree (payment_allocation_id) WHERE (status = ANY (ARRAY['created'::text, 'pending'::text, 'requires_action'::text, 'unknown'::text]));


--
-- Name: payouts_pending_reconciliation_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX payouts_pending_reconciliation_idx ON public.payouts USING btree (updated_at) WHERE (status = ANY (ARRAY['created'::text, 'pending'::text, 'requires_action'::text, 'unknown'::text]));


--
-- Name: payouts_provider_reference_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX payouts_provider_reference_key ON public.payouts USING btree (provider, provider_reference) WHERE (provider_reference <> ''::text);


--
-- Name: payouts_reconciliation_claim_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX payouts_reconciliation_claim_idx ON public.payouts USING btree (updated_at) WHERE (status = ANY (ARRAY['created'::text, 'pending'::text, 'requires_action'::text, 'unknown'::text]));


--
-- Name: promotion_redemptions_client_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX promotion_redemptions_client_id_idx ON public.promotion_redemptions USING btree (client_id, created_at DESC);


--
-- Name: promotion_redemptions_customer_email_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX promotion_redemptions_customer_email_idx ON public.promotion_redemptions USING btree (client_id, customer_email);


--
-- Name: promotion_redemptions_promotion_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX promotion_redemptions_promotion_id_idx ON public.promotion_redemptions USING btree (promotion_id, created_at DESC);


--
-- Name: promotion_sections_section_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX promotion_sections_section_id_idx ON public.promotion_sections USING btree (section_id);


--
-- Name: promotion_services_service_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX promotion_services_service_id_idx ON public.promotion_services USING btree (service_id);


--
-- Name: promotions_client_id_active_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX promotions_client_id_active_idx ON public.promotions USING btree (client_id, is_active, starts_at, ends_at);


--
-- Name: promotions_client_id_code_unique_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX promotions_client_id_code_unique_idx ON public.promotions USING btree (client_id, lower(code)) WHERE (code <> ''::text);


--
-- Name: promotions_client_id_type_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX promotions_client_id_type_idx ON public.promotions USING btree (client_id, promotion_type, updated_at DESC);


--
-- Name: provider_availability_windows_client_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX provider_availability_windows_client_id_idx ON public.provider_availability_windows USING btree (client_id);


--
-- Name: provider_portfolio_items_client_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX provider_portfolio_items_client_id_idx ON public.provider_portfolio_items USING btree (client_id, sort_order);


--
-- Name: provider_reviews_client_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX provider_reviews_client_id_idx ON public.provider_reviews USING btree (client_id, created_at DESC);


--
-- Name: provider_settlement_evidence_unmatched_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX provider_settlement_evidence_unmatched_idx ON public.provider_settlement_evidence USING btree (observed_at) WHERE (status = 'unmatched'::text);


--
-- Name: provider_webhook_events_pending_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX provider_webhook_events_pending_idx ON public.provider_webhook_events USING btree (next_attempt_at, received_at) WHERE (processing_status = ANY (ARRAY['pending'::text, 'failed'::text]));


--
-- Name: provider_webhook_events_provider_event_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX provider_webhook_events_provider_event_key ON public.provider_webhook_events USING btree (provider, provider_event_id) WHERE (provider_event_id <> ''::text);


--
-- Name: resolved_locations_expiry_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX resolved_locations_expiry_idx ON public.resolved_locations USING btree (expires_at);


--
-- Name: service_availability_windows_service_day_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX service_availability_windows_service_day_idx ON public.service_availability_windows USING btree (service_id, day_of_week, start_time);


--
-- Name: service_sections_client_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX service_sections_client_id_idx ON public.service_sections USING btree (client_id);


--
-- Name: service_sections_client_id_sort_order_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX service_sections_client_id_sort_order_idx ON public.service_sections USING btree (client_id, sort_order);


--
-- Name: service_short_notice_rules_service_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX service_short_notice_rules_service_idx ON public.service_short_notice_rules USING btree (service_id, threshold_minutes);


--
-- Name: service_wizard_drafts_client_updated_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX service_wizard_drafts_client_updated_idx ON public.service_wizard_drafts USING btree (client_id, updated_at DESC);


--
--
-- Name: service_wizard_drafts_one_per_service_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX service_wizard_drafts_one_per_service_idx ON public.service_wizard_drafts USING btree (client_id, service_id) WHERE (service_id IS NOT NULL);


--
-- Name: services_client_id_agreement_template_family_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX services_client_id_agreement_template_family_id_idx ON public.services USING btree (client_id, agreement_template_family_id);


--
-- Name: services_client_id_category_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX services_client_id_category_idx ON public.services USING btree (client_id, category);


--
-- Name: services_client_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX services_client_id_idx ON public.services USING btree (client_id);


--
-- Name: services_client_id_section_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX services_client_id_section_id_idx ON public.services USING btree (client_id, section_id);


--
-- Name: services_client_id_status_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX services_client_id_status_idx ON public.services USING btree (client_id, status);


--
-- Name: payments payments_notify_status_change; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER payments_notify_status_change AFTER UPDATE OF status, version ON public.payments FOR EACH ROW WHEN ((old.version IS DISTINCT FROM new.version)) EXECUTE FUNCTION public.notify_payment_status_change();


--
-- Name: agreement_acceptances agreement_acceptances_agreement_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agreement_acceptances
    ADD CONSTRAINT agreement_acceptances_agreement_id_fkey FOREIGN KEY (agreement_id) REFERENCES public.agreement_instances(id) ON DELETE CASCADE;


--
-- Name: agreement_events agreement_events_agreement_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agreement_events
    ADD CONSTRAINT agreement_events_agreement_id_fkey FOREIGN KEY (agreement_id) REFERENCES public.agreement_instances(id) ON DELETE CASCADE;


--
-- Name: agreement_instances agreement_instances_booking_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agreement_instances
    ADD CONSTRAINT agreement_instances_booking_id_fkey FOREIGN KEY (booking_id) REFERENCES public.bookings(id) ON DELETE SET NULL;


--
-- Name: agreement_instances agreement_instances_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agreement_instances
    ADD CONSTRAINT agreement_instances_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: agreement_instances agreement_instances_customer_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agreement_instances
    ADD CONSTRAINT agreement_instances_customer_id_fkey FOREIGN KEY (customer_id) REFERENCES public.customers(id) ON DELETE SET NULL;


--
-- Name: agreement_instances agreement_instances_template_family_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agreement_instances
    ADD CONSTRAINT agreement_instances_template_family_id_fkey FOREIGN KEY (template_family_id) REFERENCES public.agreement_template_families(id) ON DELETE RESTRICT;


--
-- Name: agreement_instances agreement_instances_template_version_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agreement_instances
    ADD CONSTRAINT agreement_instances_template_version_id_fkey FOREIGN KEY (template_version_id) REFERENCES public.agreement_template_versions(id) ON DELETE RESTRICT;


--
-- Name: agreement_jobs agreement_jobs_agreement_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agreement_jobs
    ADD CONSTRAINT agreement_jobs_agreement_id_fkey FOREIGN KEY (agreement_id) REFERENCES public.agreement_instances(id) ON DELETE CASCADE;


--
-- Name: agreement_template_families agreement_template_families_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agreement_template_families
    ADD CONSTRAINT agreement_template_families_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: agreement_template_families agreement_template_families_created_by_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agreement_template_families
    ADD CONSTRAINT agreement_template_families_created_by_client_id_fkey FOREIGN KEY (created_by_client_id) REFERENCES public.clients(id) ON DELETE SET NULL;


--
-- Name: agreement_template_families agreement_template_families_current_version_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agreement_template_families
    ADD CONSTRAINT agreement_template_families_current_version_fkey FOREIGN KEY (id, current_published_version_id) REFERENCES public.agreement_template_versions(family_id, id) ON DELETE RESTRICT;


--
-- Name: agreement_template_families agreement_template_families_source_family_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agreement_template_families
    ADD CONSTRAINT agreement_template_families_source_family_id_fkey FOREIGN KEY (source_family_id) REFERENCES public.agreement_template_families(id) ON DELETE SET NULL;


--
-- Name: agreement_template_generation_jobs agreement_template_generation_jobs_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agreement_template_generation_jobs
    ADD CONSTRAINT agreement_template_generation_jobs_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: agreement_template_generation_jobs agreement_template_generation_jobs_family_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agreement_template_generation_jobs
    ADD CONSTRAINT agreement_template_generation_jobs_family_id_fkey FOREIGN KEY (family_id) REFERENCES public.agreement_template_families(id) ON DELETE CASCADE;


--
-- Name: agreement_template_generation_jobs agreement_template_generation_jobs_family_version_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agreement_template_generation_jobs
    ADD CONSTRAINT agreement_template_generation_jobs_family_version_fkey FOREIGN KEY (family_id, version_id) REFERENCES public.agreement_template_versions(family_id, id) ON DELETE CASCADE;


--
-- Name: agreement_template_generation_jobs agreement_template_generation_jobs_version_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agreement_template_generation_jobs
    ADD CONSTRAINT agreement_template_generation_jobs_version_id_fkey FOREIGN KEY (version_id) REFERENCES public.agreement_template_versions(id) ON DELETE CASCADE;


--
-- Name: agreement_template_versions agreement_template_versions_created_by_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agreement_template_versions
    ADD CONSTRAINT agreement_template_versions_created_by_client_id_fkey FOREIGN KEY (created_by_client_id) REFERENCES public.clients(id) ON DELETE SET NULL;


--
-- Name: agreement_template_versions agreement_template_versions_family_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agreement_template_versions
    ADD CONSTRAINT agreement_template_versions_family_id_fkey FOREIGN KEY (family_id) REFERENCES public.agreement_template_families(id) ON DELETE CASCADE;


--
-- Name: auth_password_reset_tokens auth_password_reset_tokens_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.auth_password_reset_tokens
    ADD CONSTRAINT auth_password_reset_tokens_user_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: auth_refresh_sessions auth_refresh_sessions_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.auth_refresh_sessions
    ADD CONSTRAINT auth_refresh_sessions_user_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: automation_settings automation_settings_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.automation_settings
    ADD CONSTRAINT automation_settings_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: booking_quote_promotions booking_quote_promotions_booking_quote_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.booking_quote_promotions
    ADD CONSTRAINT booking_quote_promotions_booking_quote_id_fkey FOREIGN KEY (booking_quote_id) REFERENCES public.booking_quotes(id) ON DELETE CASCADE;


--
-- Name: booking_quote_promotions booking_quote_promotions_promotion_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.booking_quote_promotions
    ADD CONSTRAINT booking_quote_promotions_promotion_id_fkey FOREIGN KEY (promotion_id) REFERENCES public.promotions(id) ON DELETE RESTRICT;


--
-- Name: booking_quotes booking_quotes_agreement_template_family_id_snapshot_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.booking_quotes
    ADD CONSTRAINT booking_quotes_agreement_template_family_id_snapshot_fkey FOREIGN KEY (agreement_template_family_id_snapshot) REFERENCES public.agreement_template_families(id) ON DELETE RESTRICT;


--
-- Name: booking_quotes booking_quotes_agreement_template_version_id_snapshot_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.booking_quotes
    ADD CONSTRAINT booking_quotes_agreement_template_version_id_snapshot_fkey FOREIGN KEY (agreement_template_version_id_snapshot) REFERENCES public.agreement_template_versions(id) ON DELETE RESTRICT;


--
-- Name: booking_quotes booking_quotes_booking_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.booking_quotes
    ADD CONSTRAINT booking_quotes_booking_id_fkey FOREIGN KEY (booking_id) REFERENCES public.bookings(id) ON DELETE RESTRICT;


--
-- Name: booking_quotes booking_quotes_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.booking_quotes
    ADD CONSTRAINT booking_quotes_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE RESTRICT;


--
-- Name: booking_quotes booking_quotes_promotion_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.booking_quotes
    ADD CONSTRAINT booking_quotes_promotion_id_fkey FOREIGN KEY (promotion_id) REFERENCES public.promotions(id) ON DELETE SET NULL;


--
-- Name: booking_quotes booking_quotes_service_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.booking_quotes
    ADD CONSTRAINT booking_quotes_service_id_fkey FOREIGN KEY (service_id) REFERENCES public.services(id) ON DELETE RESTRICT;


--
-- Name: booking_quotes booking_quotes_short_notice_rule_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.booking_quotes
    ADD CONSTRAINT booking_quotes_short_notice_rule_id_fkey FOREIGN KEY (short_notice_rule_id) REFERENCES public.service_short_notice_rules(id) ON DELETE SET NULL;


--
-- Name: booking_standalone_signatures booking_standalone_signatures_booking_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.booking_standalone_signatures
    ADD CONSTRAINT booking_standalone_signatures_booking_id_fkey FOREIGN KEY (booking_id) REFERENCES public.bookings(id) ON DELETE CASCADE;


--
-- Name: bookings bookings_agreement_template_family_id_snapshot_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bookings
    ADD CONSTRAINT bookings_agreement_template_family_id_snapshot_fkey FOREIGN KEY (agreement_template_family_id_snapshot) REFERENCES public.agreement_template_families(id) ON DELETE RESTRICT;


--
-- Name: bookings bookings_agreement_template_version_id_snapshot_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bookings
    ADD CONSTRAINT bookings_agreement_template_version_id_snapshot_fkey FOREIGN KEY (agreement_template_version_id_snapshot) REFERENCES public.agreement_template_versions(id) ON DELETE RESTRICT;


--
-- Name: bookings bookings_booking_quote_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bookings
    ADD CONSTRAINT bookings_booking_quote_id_fkey FOREIGN KEY (booking_quote_id) REFERENCES public.booking_quotes(id) ON DELETE RESTRICT;


--
-- Name: bookings bookings_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bookings
    ADD CONSTRAINT bookings_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: bookings bookings_customer_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bookings
    ADD CONSTRAINT bookings_customer_id_fkey FOREIGN KEY (customer_id) REFERENCES public.customers(id) ON DELETE CASCADE;


--
-- Name: bookings bookings_promotion_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bookings
    ADD CONSTRAINT bookings_promotion_id_fkey FOREIGN KEY (promotion_id) REFERENCES public.promotions(id) ON DELETE SET NULL;


--
-- Name: bookings bookings_service_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bookings
    ADD CONSTRAINT bookings_service_id_fkey FOREIGN KEY (service_id) REFERENCES public.services(id) ON DELETE SET NULL;


--
-- Name: bookings bookings_short_notice_rule_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bookings
    ADD CONSTRAINT bookings_short_notice_rule_id_fkey FOREIGN KEY (short_notice_rule_id) REFERENCES public.service_short_notice_rules(id) ON DELETE SET NULL;


--
-- Name: business_balance_entries business_balance_entries_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.business_balance_entries
    ADD CONSTRAINT business_balance_entries_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE RESTRICT;


--
-- Name: business_balance_entries business_balance_entries_payment_adjustment_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.business_balance_entries
    ADD CONSTRAINT business_balance_entries_payment_adjustment_id_fkey FOREIGN KEY (payment_adjustment_id) REFERENCES public.payment_adjustments(id) ON DELETE RESTRICT;


--
-- Name: business_locations business_locations_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.business_locations
    ADD CONSTRAINT business_locations_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: client_profile_handles client_profile_handles_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.client_profile_handles
    ADD CONSTRAINT client_profile_handles_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: client_profiles client_profiles_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.client_profiles
    ADD CONSTRAINT client_profiles_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: client_profiles client_profiles_current_handle_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.client_profiles
    ADD CONSTRAINT client_profiles_current_handle_fkey FOREIGN KEY (client_id, handle_slug) REFERENCES public.client_profile_handles(client_id, handle_slug);


--
-- Name: customers customers_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.customers
    ADD CONSTRAINT customers_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: inbox_conversations inbox_conversations_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_conversations
    ADD CONSTRAINT inbox_conversations_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: inbox_conversations inbox_conversations_customer_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_conversations
    ADD CONSTRAINT inbox_conversations_customer_id_fkey FOREIGN KEY (customer_id) REFERENCES public.customers(id) ON DELETE SET NULL;


--
-- Name: inbox_messages inbox_messages_conversation_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_messages
    ADD CONSTRAINT inbox_messages_conversation_id_fkey FOREIGN KEY (conversation_id) REFERENCES public.inbox_conversations(id) ON DELETE CASCADE;


--
-- Name: notifications notifications_booking_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.notifications
    ADD CONSTRAINT notifications_booking_id_fkey FOREIGN KEY (booking_id) REFERENCES public.bookings(id) ON DELETE SET NULL;


--
-- Name: notifications notifications_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.notifications
    ADD CONSTRAINT notifications_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: notifications notifications_customer_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.notifications
    ADD CONSTRAINT notifications_customer_id_fkey FOREIGN KEY (customer_id) REFERENCES public.customers(id) ON DELETE SET NULL;


--
-- Name: payment_adjustments payment_adjustments_payment_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payment_adjustments
    ADD CONSTRAINT payment_adjustments_payment_id_fkey FOREIGN KEY (payment_id) REFERENCES public.payments(id) ON DELETE RESTRICT;


--
-- Name: payment_allocations payment_allocations_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payment_allocations
    ADD CONSTRAINT payment_allocations_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE RESTRICT;


--
-- Name: payment_allocations payment_allocations_payment_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payment_allocations
    ADD CONSTRAINT payment_allocations_payment_id_fkey FOREIGN KEY (payment_id) REFERENCES public.payments(id) ON DELETE RESTRICT;


--
-- Name: payment_exceptions payment_exceptions_booking_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payment_exceptions
    ADD CONSTRAINT payment_exceptions_booking_id_fkey FOREIGN KEY (booking_id) REFERENCES public.bookings(id) ON DELETE RESTRICT;


--
-- Name: payment_exceptions payment_exceptions_payment_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payment_exceptions
    ADD CONSTRAINT payment_exceptions_payment_id_fkey FOREIGN KEY (payment_id) REFERENCES public.payments(id) ON DELETE RESTRICT;


--
-- Name: payments payments_booking_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payments
    ADD CONSTRAINT payments_booking_id_fkey FOREIGN KEY (booking_id) REFERENCES public.bookings(id) ON DELETE RESTRICT;


--
-- Name: payments payments_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payments
    ADD CONSTRAINT payments_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE RESTRICT;


--
-- Name: payments payments_customer_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payments
    ADD CONSTRAINT payments_customer_id_fkey FOREIGN KEY (customer_id) REFERENCES public.customers(id) ON DELETE RESTRICT;


--
-- Name: payout_destinations payout_destinations_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payout_destinations
    ADD CONSTRAINT payout_destinations_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE RESTRICT;


--
-- Name: payouts payouts_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payouts
    ADD CONSTRAINT payouts_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE RESTRICT;


--
-- Name: payouts payouts_payment_allocation_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payouts
    ADD CONSTRAINT payouts_payment_allocation_id_fkey FOREIGN KEY (payment_allocation_id) REFERENCES public.payment_allocations(id) ON DELETE RESTRICT;


--
-- Name: payouts payouts_payout_destination_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payouts
    ADD CONSTRAINT payouts_payout_destination_id_fkey FOREIGN KEY (payout_destination_id) REFERENCES public.payout_destinations(id) ON DELETE RESTRICT;


--
-- Name: promotion_redemptions promotion_redemptions_booking_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.promotion_redemptions
    ADD CONSTRAINT promotion_redemptions_booking_id_fkey FOREIGN KEY (booking_id) REFERENCES public.bookings(id) ON DELETE SET NULL;


--
-- Name: promotion_redemptions promotion_redemptions_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.promotion_redemptions
    ADD CONSTRAINT promotion_redemptions_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: promotion_redemptions promotion_redemptions_customer_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.promotion_redemptions
    ADD CONSTRAINT promotion_redemptions_customer_id_fkey FOREIGN KEY (customer_id) REFERENCES public.customers(id) ON DELETE SET NULL;


--
-- Name: promotion_redemptions promotion_redemptions_promotion_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.promotion_redemptions
    ADD CONSTRAINT promotion_redemptions_promotion_id_fkey FOREIGN KEY (promotion_id) REFERENCES public.promotions(id) ON DELETE CASCADE;


--
-- Name: promotion_sections promotion_sections_promotion_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.promotion_sections
    ADD CONSTRAINT promotion_sections_promotion_id_fkey FOREIGN KEY (promotion_id) REFERENCES public.promotions(id) ON DELETE CASCADE;


--
-- Name: promotion_sections promotion_sections_section_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.promotion_sections
    ADD CONSTRAINT promotion_sections_section_id_fkey FOREIGN KEY (section_id) REFERENCES public.service_sections(id) ON DELETE CASCADE;


--
-- Name: promotion_services promotion_services_promotion_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.promotion_services
    ADD CONSTRAINT promotion_services_promotion_id_fkey FOREIGN KEY (promotion_id) REFERENCES public.promotions(id) ON DELETE CASCADE;


--
-- Name: promotion_services promotion_services_service_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.promotion_services
    ADD CONSTRAINT promotion_services_service_id_fkey FOREIGN KEY (service_id) REFERENCES public.services(id) ON DELETE CASCADE;


--
-- Name: promotions promotions_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.promotions
    ADD CONSTRAINT promotions_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: provider_availability_windows provider_availability_windows_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_availability_windows
    ADD CONSTRAINT provider_availability_windows_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: provider_portfolio_items provider_portfolio_items_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_portfolio_items
    ADD CONSTRAINT provider_portfolio_items_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: provider_portfolio_items provider_portfolio_items_service_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_portfolio_items
    ADD CONSTRAINT provider_portfolio_items_service_id_fkey FOREIGN KEY (service_id) REFERENCES public.services(id) ON DELETE SET NULL;


--
-- Name: provider_reviews provider_reviews_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_reviews
    ADD CONSTRAINT provider_reviews_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: provider_reviews provider_reviews_customer_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_reviews
    ADD CONSTRAINT provider_reviews_customer_id_fkey FOREIGN KEY (customer_id) REFERENCES public.customers(id) ON DELETE SET NULL;


--
-- Name: provider_settlement_evidence provider_settlement_evidence_payment_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_settlement_evidence
    ADD CONSTRAINT provider_settlement_evidence_payment_id_fkey FOREIGN KEY (payment_id) REFERENCES public.payments(id) ON DELETE RESTRICT;


--
-- Name: service_availability_windows service_availability_windows_service_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.service_availability_windows
    ADD CONSTRAINT service_availability_windows_service_id_fkey FOREIGN KEY (service_id) REFERENCES public.services(id) ON DELETE CASCADE;


--
-- Name: service_sections service_sections_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.service_sections
    ADD CONSTRAINT service_sections_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: service_short_notice_rules service_short_notice_rules_service_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.service_short_notice_rules
    ADD CONSTRAINT service_short_notice_rules_service_id_fkey FOREIGN KEY (service_id) REFERENCES public.services(id) ON DELETE CASCADE;


--
-- Name: service_wizard_drafts service_wizard_drafts_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.service_wizard_drafts
    ADD CONSTRAINT service_wizard_drafts_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: service_wizard_drafts service_wizard_drafts_service_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.service_wizard_drafts
    ADD CONSTRAINT service_wizard_drafts_service_id_fkey FOREIGN KEY (service_id) REFERENCES public.services(id) ON DELETE CASCADE;


--
-- Name: services services_agreement_template_family_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.services
    ADD CONSTRAINT services_agreement_template_family_id_fkey FOREIGN KEY (agreement_template_family_id) REFERENCES public.agreement_template_families(id) ON DELETE SET NULL;


--
-- Name: services services_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.services
    ADD CONSTRAINT services_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: services services_client_provider_location_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.services
    ADD CONSTRAINT services_client_provider_location_fk FOREIGN KEY (client_id, provider_location_id) REFERENCES public.business_locations(client_id, id) ON DELETE RESTRICT;


--
-- Name: services services_section_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.services
    ADD CONSTRAINT services_section_id_fkey FOREIGN KEY (section_id) REFERENCES public.service_sections(id) ON DELETE SET NULL;


--
-- PostgreSQL database dump complete
--


--
-- Dbmate schema migrations
--

INSERT INTO public.schema_migrations (version) VALUES
    ('20260808140000');

-- Read-only preflight for 20260905020000_auth_identity_foundation.sql.
-- Identifiers are represented by hashes so this output is safe to retain with test evidence.
SELECT 'provider_unverified_email' AS finding, count(*) AS affected
FROM clients
WHERE email IS NOT NULL AND btrim(email) <> '' AND email_verified_at IS NULL
UNION ALL
SELECT 'provider_blank_email', count(*)
FROM clients
WHERE email IS NOT NULL AND btrim(email) = ''
UNION ALL
SELECT 'provider_normalized_email_collision', count(*)
FROM (
    SELECT lower(btrim(email))
    FROM clients
    WHERE email IS NOT NULL AND btrim(email) <> ''
    GROUP BY lower(btrim(email))
    HAVING count(*) > 1
) collision
UNION ALL
SELECT 'provider_verified_email_missing_identity', count(*)
FROM clients client
WHERE client.email IS NOT NULL
  AND client.email_verified_at IS NOT NULL
  AND NOT EXISTS (
      SELECT 1
      FROM provider_auth_identities identity
      WHERE identity.client_id = client.id
        AND identity.identity_type = 'email'
        AND identity.normalized_identifier = lower(btrim(client.email))
  )
UNION ALL
SELECT 'marketplace_normalized_email_collision', count(*)
FROM (
    SELECT lower(btrim(email))
    FROM marketplace_customers
    WHERE email IS NOT NULL AND btrim(email) <> ''
    GROUP BY lower(btrim(email))
    HAVING count(*) > 1
) collision
UNION ALL
SELECT 'marketplace_verified_email_missing_identity', count(*)
FROM marketplace_customers customer
WHERE customer.email IS NOT NULL
  AND customer.email_verified_at IS NOT NULL
  AND NOT EXISTS (
      SELECT 1
      FROM marketplace_customer_identities identity
      WHERE identity.marketplace_customer_id = customer.id
        AND identity.identifier_type = 'email'
        AND identity.normalized_identifier = lower(btrim(customer.email))
  )
UNION ALL
SELECT 'marketplace_phone_multiple_owners', count(*)
FROM (
    SELECT value
    FROM (
        SELECT id AS customer_id, phone_e164 AS value
        FROM marketplace_customers
        WHERE phone_e164 IS NOT NULL AND btrim(phone_e164) <> ''
        UNION ALL
        SELECT id, NULLIF(to_jsonb(customer)->>'whatsapp_e164', '')
        FROM marketplace_customers customer
        WHERE NULLIF(to_jsonb(customer)->>'whatsapp_e164', '') IS NOT NULL
        UNION ALL
        SELECT marketplace_customer_id, normalized_identifier
        FROM marketplace_customer_identities
        WHERE identifier_type IN ('phone', 'whatsapp')
    ) contact
    GROUP BY value
    HAVING count(DISTINCT customer_id) > 1
) collision
UNION ALL
SELECT 'marketplace_customer_multiple_phones', count(*)
FROM (
    SELECT customer_id
    FROM (
        SELECT id AS customer_id, phone_e164 AS value
        FROM marketplace_customers
        WHERE phone_e164 IS NOT NULL AND btrim(phone_e164) <> ''
        UNION ALL
        SELECT id, NULLIF(to_jsonb(customer)->>'whatsapp_e164', '')
        FROM marketplace_customers customer
        WHERE NULLIF(to_jsonb(customer)->>'whatsapp_e164', '') IS NOT NULL
        UNION ALL
        SELECT marketplace_customer_id, normalized_identifier
        FROM marketplace_customer_identities
        WHERE identifier_type IN ('phone', 'whatsapp')
    ) contact
    GROUP BY customer_id
    HAVING count(DISTINCT value) > 1
) collision;

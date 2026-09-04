# Edge delivery and API routing

Tellbook's production browser contract is same-origin: both web applications call `/v1/*` on
their own public origin. The ingress must send those requests directly to the Go API and send all
other requests to the corresponding SvelteKit service. This avoids holding a second Node
connection for every API request or SSE stream while retaining same-origin cookies.

## Required production routes

Apply this routing order to both the provider and marketplace hostnames:

1. the SSE locations below go directly to the Go API with response buffering and compression
   disabled;
2. every other `/v1/*` request goes directly to the Go API;
3. every non-API request goes to that hostname's SvelteKit service.

The SvelteKit services should set `PRIVATE_API_BASE_URL` to the private Go service origin, for
example `http://tellbook-api:8200`. Server-side `fetch('/v1/...')` calls are then sent directly to
that origin while browser calls remain relative. The value must be an HTTP(S) origin without
credentials, path, query, or fragment.

`PUBLIC_API_BASE_URL` is a local-development proxy target. The marketplace's Node proxy is
disabled in production by default. `API_PROXY_MODE=emergency` can temporarily re-enable it during
an ingress incident; remove that override after the incident because it adds an application hop
and doubles held connections for SSE. The provider app has no production Node API proxy.

## Nginx reference

The deployment platform may express the same policy differently. This example shows the required
behavior; replace the upstream addresses and duplicate the server block for the second web app.

```nginx
proxy_cache_path /var/cache/nginx/tellbook levels=1:2 keys_zone=tellbook_public:32m
    max_size=2g inactive=30m use_temp_path=off;

map "$http_authorization:$http_cookie" $tellbook_session_cache_bypass {
    default 1;
    ":"     0;
}

upstream tellbook_api {
    server tellbook-api:8200;
    keepalive 128;
}

upstream tellbook_web {
    server tellbook-marketplace:3000;
    keepalive 32;
}

server {
    listen 443 ssl http2;
    server_name marketplace.example.com;

    # Exact durable-event streams. Keep this regex above the ordinary /v1/ location.
    location ~ ^/v1/(app/(inbox|bookings|tessa)/events|marketplace/conversations/events|public/payments/[^/]+/events)$ {
        proxy_pass http://tellbook_api;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Connection "";
        proxy_buffering off;
        proxy_cache off;
        gzip off;
        add_header X-Accel-Buffering no always;
        proxy_read_timeout 75s;
        proxy_send_timeout 75s;
    }

    location /v1/ {
        proxy_pass http://tellbook_api;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Connection "";
        proxy_cache tellbook_public;
        proxy_cache_methods GET HEAD;
        proxy_cache_bypass $tellbook_session_cache_bypass;
        proxy_no_cache $tellbook_session_cache_bypass $upstream_http_set_cookie;
        proxy_cache_revalidate on;
        proxy_cache_background_update on;
        proxy_cache_use_stale updating;
        add_header X-Tellbook-Cache $upstream_cache_status always;
        gzip on;
        gzip_vary on;
        gzip_types application/json application/problem+json;
    }

    location / {
        proxy_pass http://tellbook_web;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Connection "";
    }
}
```

Both SvelteKit builds generate `.br` and `.gz` variants for compressible static assets. The Node
adapter selects them from the request's `Accept-Encoding`; the CDN should preserve `Vary:
Accept-Encoding`. Prefer CDN Brotli for JSON when available and keep ingress gzip as the baseline.

## Required Cloudflare cache rules

Cloudflare does not use arbitrary `Vary` fields in its cache key unless its Vary Cache Rules
setting is configured. Apply these rules to both public web hostnames before enabling API caching,
in this order:

1. Bypass cache when the request contains `Authorization` or `Cookie`. This rule must run before
   lookup so a session-bearing request cannot consume an already-warmed anonymous object. Do not
   add the raw cookie or authorization value to the cache key.
2. Make only the anonymous endpoint allowlist below eligible for cache and keep Origin Cache
   Control enabled. Do not create a general `/v1/*`, `/v1/public/*`, or GET cache rule.
3. Configure Vary actions for the allowlist: `authorization=bypass`, `cookie=bypass`, and
   `origin=passthrough`. The first rule remains the primary request-time boundary; these Vary
   actions make the cached response contract explicit as well.

`CDN-Cache-Control` carries the shared-cache TTL while ordinary `Cache-Control` carries the browser
TTL. Neither header combines `s-maxage` with `stale-while-revalidate`, because Cloudflare treats
`s-maxage` as `proxy-revalidate` and will not serve stale content in that combination.

## Cache boundary

The edge must honor the API's `Cache-Control`, `ETag`, and `Vary` headers. It must not override
`private` or `no-store`, cache `Set-Cookie` responses, or include authorization/cookie-bearing
requests in a shared cache. In particular, never publicly cache authentication, customer or
provider data, location-token results, availability, booking, agreement, payment, receipt,
conversation, notification, or mutation responses.

Only endpoints with an explicit public policy are eligible for shared caching. The server's
default remains `no-store`; do not add a broad `/v1/public/*` or GET caching rule at the edge.

The explicit anonymous allowlist is:

- `/v1/marketplace/categories` and `/v1/marketplace/regions`: 60-second browser TTL, five-minute
  shared TTL, and ten-minute stale-while-revalidate window;
- `/v1/public/clients/{slug}`, `/services`, and `/reviews`: 15-second browser TTL, 30-second shared
  TTL, and 60-second stale-while-revalidate window;
- `/v1/marketplace/home` and `/v1/marketplace/providers`: five-second browser TTL, 15-second shared
  TTL, and 30-second stale-while-revalidate window, but only without `location_token`; provider
  search also requires `available_on` to be absent.

All of these responses use request-scoped ETags. Provider resources include a durable revision
advanced by public profile, handle, service, schedule, location, portfolio, review, completed
booking, and provider-owned agreement mutations, plus a representation-contract version. This
lets a matching conditional request return before the larger public payload is queried or
serialized. Discovery responses include the existing provider/service/availability projection
revisions and a final representation digest because they are assembled from multiple providers.
Timestamps are not used as validators.

Requests carrying either `Authorization` or `Cookie` receive `private, no-store` without an ETag,
even for an allowlisted route. Anonymous cacheable responses vary on `Origin`, `Authorization`,
and `Cookie`; the required Cloudflare bypass/Vary rules above enforce that boundary before cache
lookup. GPS-token discovery, date-filtered availability discovery, public slot availability,
bookings, payments, and every mutation retain `no-store`.

## Public image delivery

Configure `R2_PUBLIC_BUCKET_NAME` and the HTTPS `R2_PUBLIC_BUCKET_BASE_URL` together. Provider
profile, service, section, and portfolio uploads are written to that public bucket under a new
UUID object key for every upload. The stored URL is therefore stable and naturally versioned;
public objects carry `Cache-Control: public, max-age=31536000, immutable`. Agreement PDFs and all
other documents continue to use the private bucket and short signed URLs.

Enable Cloudflare Image Resizing only on the public media hostname and the `/clients/*` object
path. Set both web applications' `PUBLIC_MEDIA_BASE_URL` to the same value as the API's
`R2_PUBLIC_BUCKET_BASE_URL`. Their SSR image components retain the canonical API URL as data but
render `/cdn-cgi/image` URLs with `format=auto`, quality 82, and the bounded
`160;320;640;960;1280` responsive widths. Cloudflare caches each negotiated transformed result.
This avoids a second variant-processing queue and extra media rows while preventing oversized
originals from reaching cards.

Existing development images stored in the private bucket should be reseeded or re-uploaded. Do
not keep signing those URLs into otherwise public-cacheable payloads as a compatibility path.

## Deployment checks

- A browser request to `/v1/healthz` or another API route reaches the Go service without a
  SvelteKit proxy header or process hop.
- Each SSE endpoint returns `Content-Type: text/event-stream`, `Cache-Control: no-store`, and
  `X-Accel-Buffering: no`; comments arrive without buffering.
- Repeated category, region, profile, service, review, and eligible discovery requests with
  `If-None-Match` return `304`; a relevant provider mutation changes the validator.
- The same allowlisted request with `Authorization` or `Cookie`, plus GPS-token and date-filtered
  discovery requests, is an edge `BYPASS` and returns `private, no-store` without an ETag.
- A hashed Svelte asset requested with `Accept-Encoding: br` is served compressed and carries
  `Vary: Accept-Encoding` plus an immutable asset cache policy.
- A public card image is served from `R2_PUBLIC_BUCKET_BASE_URL`, carries immutable origin
  metadata, and the image-delivery response varies by negotiated format/width without changing
  the canonical URL stored in PostgreSQL.
- Removing the Go `/v1/*` ingress route causes an obvious deployment failure; do not silently leave
  the marketplace emergency proxy enabled as the steady-state topology.

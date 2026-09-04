# Google-derived location data audit

Audited 2026-08-31 against the enabled server flows and Google's then-current official terms.
This is an engineering compliance gate, not legal advice. Recheck the applicable billing-account
terms before release because EEA and non-EEA terms can differ.

## Current data flow

| Flow | Provider | Current storage | Assessment |
| --- | --- | --- | --- |
| Public location token | Manual input, browser GPS, optional Google Geocoding/Places | `resolved_locations`; token expires after 30 minutes | Access expires correctly, but no deletion job removes expired Google content. Expired rows can therefore remain stored. |
| Provider business location | Manual input, browser GPS, Google Place Details, or Geocoding | `business_locations`; persistent address, place ID, coordinates, locality and region IDs | Place IDs are safe to retain. Persistent Google-derived Places coordinates need a 30-day refresh/delete rule. Geocoding's longer end-user-specific exception must not be assumed for marketplace-wide reuse. |
| Booking/quote location snapshot | Copied from provider/customer location | `booking_quotes` and `bookings`; persistent transaction record | Provenance is not retained, so Google-derived coordinates cannot currently be distinguished from user/browser data for retention enforcement. |
| State/LGA inference | PostGIS point-in-polygon over downloaded geoBoundaries data | Canonical region foreign keys and CC BY 4.0 polygons | Independent from Google content. Keep source/version/license attribution in `administrative_regions` and do not derive these polygons from Google. |

The active code requests `formattedAddress`, `location`, and `addressComponents` from Places, and
formatted address, geometry, and address components from Geocoding. It then stores the results in
the tables above.

## Applicable rules

- Google states that place IDs are exempt from caching restrictions and may be stored for later
  use; it recommends refreshing IDs older than 12 months:
  <https://developers.google.com/maps/documentation/places/web-service/place-id>.
- The current Places service terms limit cached latitude/longitude to 30 consecutive days:
  <https://cloud.google.com/maps-platform/terms/maps-service-terms>.
- The current Geocoding terms also specify a 30-day general limit, with a narrower indefinite
  exception only for direct end-user-facing functionality, logically isolated to the associated
  end user, and not used to replace another service call. Marketplace-wide provider discovery
  should not rely on that exception without confirmation from counsel/account terms:
  <https://cloud.google.com/maps-platform/terms/maps-service-terms>.
- Places and Geocoding content shown without a Google map needs Google Maps attribution. Content
  shown on a map must follow the map-provider compatibility rule; Google-derived content must not
  be combined with a non-Google map unless the specific service terms allow it:
  <https://developers.google.com/maps/documentation/places/web-service/policies> and
  <https://developers.google.com/maps/documentation/geocoding/policies>.
- A street address selected by an end user through Places Autocomplete can qualify as end-user
  data for that user's specific transaction under Google's documented exception. This does not
  cover suggestion lists, POI lookup, or unrelated reuse.

## Required changes before release or another location cache

1. Record provenance separately for address text and coordinates: `browser_gps`, `user_entered`,
   `user_confirmed_autocomplete`, `google_geocoding`, or `google_places`, plus `resolved_at`.
2. Retain place IDs, but refresh or delete Google Places/Geocoding coordinates by the applicable
   30-day deadline unless the exact account terms and use case provide a documented exception.
3. Physically delete expired Google-backed `resolved_locations`; token expiry alone is not a
   storage-retention control.
4. Require explicit user confirmation before converting an autocomplete street address into
   durable user-provided address data. Do not apply that conversion to POIs automatically.
5. Define booking-record retention by provenance. Preserve the contractual transaction snapshot
   where allowed, but do not silently classify copied Google content as customer data.
6. Add visible Google Maps attribution wherever Google-derived address content is displayed
   without a Google map. If a map is added, use a compatible Google map for Google content or keep
   the map fed exclusively by independently licensed/user-provided data.
7. Keep provider-nearby and state/LGA filtering based on browser/user-confirmed coordinates and
   the CC BY 4.0 geoBoundaries polygons. Include geoBoundaries attribution in public legal/data
   notices.

Until these items are resolved, do not add Redis or database caches for Google response payloads.
The existing short-lived resolution endpoint may continue in development, but expired rows must
not be treated as compliant merely because API access to them is blocked.


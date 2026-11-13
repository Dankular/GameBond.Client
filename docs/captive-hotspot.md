# Captive hotspot portal

The hotspot feature is an opt-in IPv4 access gate for selected `lan_devices` on
the Go client. Other connected devices retain their existing behavior. Guests
receive DHCP option 114 (RFC 8910) with the portal URL; the portal also
implements common captive-check paths. HTTPS traffic is never intercepted.

## Configure

In **Settings → Captive hotspot portal**, choose the guest adapters and set the
HTTPS listener, DNS hostname, certificate and key paths, branding, and session
duration. Save and restart the client. The certificate must cover the configured
hostname. Passwords are Argon2id-hashed by the dashboard. The dashboard never
returns password hashes or secret values.

For external and payment integrations, secret fields accept `file:<path>`
references. Relative paths are resolved from the client configuration folder;
files must be regular files no larger than 16 KiB. Direct secret values remain
accepted for compatibility, so file references are preferred.

The equivalent configuration is:

```json
{
  "hotspot": {
    "enabled": true,
    "devices": ["Guest AP"],
    "listen": "0.0.0.0:8444",
    "hostname": "wifi.example.net",
    "tls_cert": "secrets/hotspot.crt",
    "tls_key": "secrets/hotspot.key",
    "brand_name": "Guest Wi-Fi",
    "logo_file": "assets/guest-logo.png",
    "primary_color": "#185adb",
    "terms_url": "https://example.net/terms",
    "session_seconds": 28800,
    "password_hash": "argon2id$v=19$m=65536,t=2,p=2$...$...",
    "vouchers": [{"hash": "<sha256-of-voucher>", "uses": 1, "expires_at": 0}],
    "external_url": "https://identity.example.net/guest",
    "external_secret": "file:secrets/external-callback.key",
    "payment_endpoint": "https://payments.example.net/gamebond/checkout",
    "payment_secret": "file:secrets/payment-api.key",
    "payment_webhook_secret": "file:secrets/payment-webhook.key",
    "payment_plan": "day-pass",
    "payment_currency": "GBP",
    "payment_amount_minor": 500
  }
}
```

Leave an authentication method unset to disable it. Vouchers are created from
the dashboard and shown once. Voucher use counts are persisted in
`hotspot-state.json`; sessions expire on client restart. The dashboard can
revoke active sessions, which takes effect within one second.

## Access policy

Before authentication, guest forwarding allows only DNS to the configured
public resolvers and HTTPS to the resolved portal, external identity, and
payment hosts. All other IPv4 destinations are denied. After authentication,
public IPv4 egress is allowed through the tunnel. Private, loopback, link-local,
multicast, host and guest subnet destinations remain denied. The gateway also
requires an active DHCP lease and revokes access when the lease is released or
its MAC identity changes.

The portal offers voucher and shared-password forms, plus optional external and
payment flows. Password login uses Argon2id and per-guest exponential delay on
failed attempts. Guest sessions use Secure, HttpOnly, SameSite cookies, CSRF
tokens, request-size limits, CSP, and no-store headers.

## External portal contract

The client redirects to the configured HTTPS URL with `state` (base64url JSON)
and `signature` (hex HMAC-SHA256 over the decoded JSON bytes). State contains
`guest`, `exp`, `nonce`, and `return`. The external server must POST JSON to the
`return` URL with `X-GameBond-Signature` (hex HMAC-SHA256 over the exact request
body) and fields `guest`, `exp`, `nonce`, `authorized`, and optional
`duration_seconds`. Only an issued, unexpired nonce for the same guest is
accepted; callback replay is rejected. The shared secret must be sent over a
server-to-server secret channel.

## Payment adapter contract

The configured endpoint receives an authenticated JSON POST with `guest`,
`plan`, `amount_minor`, `currency`, `return_url`, and `webhook_url`. It returns
`{"id":"checkout-id","checkout_url":"https://..."}`. The webhook must POST
JSON with `X-GameBond-Signature` (hex HMAC-SHA256 over the exact body) and
`id`, `checkout_id`, `guest`, `status` (`paid`), `amount_minor`, `currency`,
`plan`, `timestamp`, and optional `duration_seconds`. It must match an active
checkout, amount, currency and plan, be no more than five minutes old, and be
idempotent. A browser return never grants access. GameBond does not store card
details. This is a generic adapter contract; a provider-specific adapter still
needs to be selected and configured.

The HTTPS host in `checkout_url` is resolved and temporarily allowlisted for
pre-auth traffic. It must be a public IPv4 endpoint on port 443.

## Operational notes

- The local portal listener is separate from the loopback-only admin dashboard.
- The configured hostname must resolve to the machine running the portal and
  the network path must deliver guest TCP/443 to the listener.
- The first implementation covers fixed-address IPv4 `lan_devices`; it does
  not provide a general Wi-Fi access point, dynamic subnet DHCP pool, IPv6
  interception, or HTTPS interception.
- Active sessions and pending callbacks are held in memory; session access
  ends when the client restarts. Voucher use counts survive restart.
- Payment and external callback delivery require an operator-hosted public
  endpoint or routing arrangement that can reach this listener.

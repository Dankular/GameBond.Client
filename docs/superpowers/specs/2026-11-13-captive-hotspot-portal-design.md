# Captive Hotspot Portal Design

## Goal

Add an opt-in captive hotspot service for guest clients. Guests see a branded landing page, authenticate with a voucher or password, or complete payment through a configured payment provider. An external portal server can provide the landing and authentication flow. Authenticated guests receive internet access through the existing GameBond tunnel; unauthenticated guests remain isolated from other guests and can reach only the portal and explicitly required network services.

## Requirements

- Custom branding landing page.
- Voucher authentication.
- Payment-based authentication.
- External portal server support.
- Password authentication.
- Guest network isolation.

## Context and assumptions

The Go client already has a per-device DHCP/ARP gateway in `native-edge/lan`, an authenticated dashboard/config API, and a tunnel path for gateway traffic. The server has an HTTPS admin portal, IP forwarding, and outbound NAT. These are separate from a guest-facing portal and do not currently authenticate guests or gate their traffic.

Hotspot mode is opt-in and applies only to explicitly selected guest devices/networks. Existing `lan_devices` keep their current behavior when hotspot configuration is absent. The first implementation targets IPv4 guests connected through the existing dedicated-adapter gateway path. Payment is provider-neutral behind a server-side adapter; credentials and webhook secrets never go to clients. HTTPS portal hosting requires a valid certificate and hostname. The operator supplies the payment provider and external portal endpoint.

## Architecture

Add a `native-edge/hotspot` package for guest identity, credential verification, session expiry, authorization state, and isolation policy. The LAN gateway asks this policy before forwarding a guest packet. Before authorization it permits DHCP, ARP, DNS to the configured resolver, the local portal, and explicitly configured payment/external-portal destinations; it drops and counts all other traffic. After authorization it permits internet forwarding but continues to block guest-to-guest and guest-to-private-management traffic. Authorization is bound to the DHCP lease identity and expires; a changed MAC, released/expired lease, or expired session revokes it.

Add a guest-facing HTTPS portal service, separate from the admin portal's routes, sessions, and credentials. It serves the branded landing page, local voucher/password forms, and payment handoff. Captive-network-assistant checks and HTTP requests are redirected to the portal where technically possible. HTTPS requests are never intercepted or downgraded; clients needing access are directed by captive-network-assistant detection, DNS/bootstrap guidance, and the advertised portal URL.

The local portal can delegate presentation and identity to an external portal server using a signed, short-lived redirect token containing an opaque guest/session reference and return URL. The external server returns a signed authorization decision to a server-to-server callback. The callback is idempotent, time-limited, replay-protected, and bound to the original guest. It cannot directly choose network policy or assert an arbitrary guest address.

Payment uses a provider adapter that creates a checkout session and verifies signed provider webhooks server-side. Access is granted only after a verified successful payment event. A browser return URL alone never authorizes access. Payment plan, duration, price/currency, provider credentials and webhook secret are operator configuration; payment data is not stored by GameBond.

The existing admin portal/dashboard receives configuration and management UI for enablement, branding, allowed methods, voucher creation/revocation, password credentials, access duration, external portal settings, payment plans/provider settings, and active guest sessions. Secrets are write-only in the UI and masked after save. The user-facing portal and admin UI share neither login credentials nor session cookies.

## Proposed configuration

```json
{
  "hotspot": {
    "enabled": true,
    "portal": {"listen": "0.0.0.0:8444", "hostname": "wifi.example.com", "tls_cert": "/run/secrets/hotspot.crt", "tls_key": "/run/secrets/hotspot.key"},
    "branding": {"name": "Guest Wi-Fi", "logo_url": "/branding/logo.png", "primary_color": "#185adb", "terms_url": "https://example.com/terms"},
    "session_seconds": 28800,
    "methods": {"voucher": true, "password": true, "payment": true, "external": false},
    "external": {"base_url": "https://portal.example.net", "callback_secret": "secret-reference"},
    "payment": {"provider": "adapter-name", "secret": "secret-reference", "webhook_secret": "secret-reference", "plans": []}
  }
}
```

The exact schema and whether guest networks require a separate interface/subnet are finalized during implementation planning against the current network-layer work. Secrets must be supplied through secret files/environment references, never committed as plaintext config. Guest access must fail closed if hotspot policy cannot be loaded or validated.

## Runtime behavior

1. Validate configuration and provider/portal prerequisites before enabling hotspot mode. If disabled, keep current gateway behavior.
2. Create guest sessions keyed to a stable DHCP lease identity. Credentials are stored as salted password hashes; vouchers are high-entropy, one-time or explicitly configured multi-use credentials with expiry and usage limits.
3. Redirect captive checks and permitted pre-auth HTTP traffic to the portal without TLS interception. Expose the canonical HTTPS URL in DHCP option 114 where supported, and retain a clear manual URL for clients that do not auto-open a portal.
4. Gate every forwarded packet, not just web requests, so unauthenticated access cannot bypass the login screen by using a hard-coded IP or alternate DNS resolver.
5. On voucher/password success, or verified external/payment callback, activate a bounded guest session and update the gateway authorization policy immediately.
6. On expiry, admin revocation, lease change, or credential revocation, remove authorization and terminate subsequent guest forwarding. Report current session state and reason.
7. Isolate guests from one another, the workstation/client host, and private/admin subnets regardless of authentication state. Permit only internet egress through the existing tunnel and explicitly configured portal/payment destinations.
8. Expose counters and audit events for login success/failure, voucher redemption, payment callback, session expiry/revocation, redirects, and denied traffic, without logging passwords, voucher values, payment secrets, or session tokens.

## Security and failure handling

- Portal sessions use secure, HttpOnly, SameSite cookies, CSRF protection on state-changing requests, rate-limited credential attempts, bounded request bodies, and no-store security headers.
- Password verification uses a memory-hard password hash. Voucher values are stored hashed and compared in constant time.
- External callbacks require TLS, signature verification, timestamp bounds, nonce/replay protection, and idempotency.
- Payment confirmation is webhook-authoritative and idempotent. Invalid, late, duplicate, or mismatched events never grant access.
- Pre-auth policy fails closed. Portal/payment outage does not grant access; existing active sessions continue only until their configured expiry.
- Admin and guest portal listeners use separate configuration, cookies, routes, and access controls.
- Branding accepts bounded local assets and safe colors/text; arbitrary HTML/script injection is not allowed.
- Guest isolation is enforced in the forwarding path, not solely by client-side UI or the landing page.

## Acceptance criteria

1. With hotspot disabled, existing client gateway and admin portal behavior is unchanged.
2. With hotspot enabled, an unauthenticated guest can obtain DHCP, resolve the portal, open the branded landing page, and cannot pass arbitrary internet or private-network traffic.
3. A valid voucher or password authorizes only the matching guest lease for the configured session duration; invalid, expired, replayed, and over-limit credentials do not.
4. A configured payment plan authorizes a guest only after a valid signed provider webhook; browser redirects alone do not.
5. External portal mode redirects with an expiring signed context and accepts only a valid, matching, replay-safe server callback.
6. Authenticated guests can reach the internet through the tunnel but cannot reach another guest, the host, or private/admin networks.
7. Session expiry and admin revocation immediately restore the pre-auth packet policy.
8. Administrators can configure branding/methods/plans and manage vouchers, passwords, and sessions through the existing management UI without reading back secrets.
9. Portal failure, invalid configuration, malformed credentials, and payment/provider failure do not create an authorization bypass.

## Delivery stages

1. Define and validate hotspot configuration; implement session store, guest firewall policy, voucher/password flows, and branded local portal.
2. Add guest management UI and runtime status/audit visibility.
3. Add signed external portal redirects/callbacks.
4. Add a provider adapter and webhook-authoritative payment checkout.

Every stage remains opt-in and keeps legacy gateway behavior intact. The selected payment provider and external-portal interoperability profile must be fixed before their respective stages are implemented.

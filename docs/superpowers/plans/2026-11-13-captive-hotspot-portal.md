# Captive Hotspot Portal Implementation Plan

**Goal:** Build an opt-in captive hotspot portal with branded landing page, voucher/password/payment/external authentication, and guest network isolation.

**Architecture:** Add a `native-edge/hotspot` policy and session package, connect it to the existing `native-edge/lan.Gateway` forwarding decision, and serve a separate guest portal from the Go client. Extend strict config validation and the existing dashboard configuration surface. Keep payment and external portal credentials server-side, and grant access only on verified, matching authorization events.

**Tech Stack:** Go standard library, existing Go `lan`, `appconfig`, and `dashboard` packages, embedded/static portal assets following repository conventions.

**Spec:** `docs/superpowers/specs/2026-11-13-captive-hotspot-portal-design.md`

## Global Constraints

- Hotspot behavior is opt-in; absent configuration preserves current `lan_devices` behavior.
- IPv4 only for the first implementation and only explicitly selected guest devices.
- Do not intercept or downgrade HTTPS traffic.
- Apply packet authorization in the gateway forwarding path, not only in portal UI.
- Guests cannot reach other guests, host management, private/admin networks, or portal administration.
- Passwords, vouchers, payment secrets, callback signatures, and session tokens must not be logged or returned to dashboard clients.
- Payment browser returns do not grant access; only verified provider events do.
- External portal authorization must be signed, time-bounded, guest-bound, replay-safe, and idempotent.
- Keep all existing user changes in the working tree; do not reset, clean, or rewrite unrelated files.
- Do not add or run tests unless the user explicitly asks for tests or verification.

## Review Focus

- Unauthenticated packets sent directly to IPs or alternate DNS are denied before tunnel forwarding.
- A guest cannot reach another guest or private/admin endpoint after authentication.
- DHCP lease identity changes and session expiry/revocation remove access immediately.
- Payment success browser redirects, duplicate events, invalid signatures, and mismatched guest references never bypass webhook verification.
- External callback replay, stale tokens, wrong guest/session binding, or invalid signatures never grant access.

## Files and responsibilities

- `native-edge/hotspot/config.go`: strict hotspot config parsing and validation.
- `native-edge/hotspot/session.go`: guest sessions, expiry, revocation, and authorization binding.
- `native-edge/hotspot/credentials.go`: password hashes and voucher creation/redemption/limits.
- `native-edge/hotspot/policy.go`: pre-auth allowlist and authenticated guest isolation decision.
- `native-edge/hotspot/external.go`: signed external redirect context and callback verification.
- `native-edge/hotspot/payment.go`: provider interface, checkout session handling, signed webhook validation and idempotency.
- `native-edge/lan/gateway.go`: consult the opt-in policy before forwarding, expose/revoke session identity on lease lifecycle.
- `native-edge/appconfig/sections.go`, `native-edge/appconfig/appconfig.go`: strict role-aware schema validation and secret-reference handling.
- `native-edge/cmd/client`: start/stop the guest portal service with the client runtime and wire it to gateway/session manager.
- `native-edge/hotspot/portal.go`: separate guest HTTP(S) handlers, captive-check routes, login, voucher, password, payment, and external flows.
- `native-edge/hotspot/static/`: branded landing and login assets with restrictive CSP and no inline script.
- `native-edge/dashboard/dashboard.go`, `widget.html`, `widget_settings.js`, and a focused hotspot UI module: save validated configuration and manage vouchers/sessions without reading back secrets.
- `FEATURES.md`, `docs/lan-devices.md`, and a new `docs/captive-hotspot.md`: implemented behavior, setup, provider integration contract, and known platform limits.

## Delivery tasks

### Task 1: Configuration and guest authorization core

Add strict opt-in hotspot configuration. Validate listener addresses, TLS paths, allowed authentication methods, durations, branding inputs, payment/external settings, and unique guest device bindings before runtime startup. Implement a bounded session manager keyed by the DHCP lease identity, salted password hash verification, hashed one-time/limited-use vouchers, expiry, and revocation. Implement a policy function that allows only DHCP, ARP, configured DNS, and portal/provider bootstrap destinations before authorization; after authorization permit internet egress only and block private, host, management, and guest-to-guest destinations. Wire the policy into `Gateway` so every forwarded packet consults it and lease lifecycle events revoke or bind sessions. Keep policy disabled when hotspot configuration is absent.

**Deliverable:** Gateway authorization decisions are available and fail closed when hotspot mode is enabled; legacy mode remains unchanged.

### Task 2: Guest portal, branding, voucher, and password flows

Start a guest portal service on the configured listener, independent of dashboard/admin routes and cookies. Serve branded local landing and login pages, terms link, voucher redemption, password login, logout/status, and captive assistant detection endpoints. Use secure session cookies, CSRF protection, request bounds, credential rate limits, strict CSP, no-store, HTTPS, and safe text/color/local-image branding. Redirect plain HTTP captive requests where possible and never MITM HTTPS. Publish a canonical portal URL and DHCP option 114 only if supported by the existing DHCP encoder; otherwise document supported detection paths and manual URL.

**Deliverable:** A guest can reach the local portal before authorization and obtain a time-limited session with a valid voucher or password.

### Task 3: External portal integration

Implement expiring signed redirect state with an opaque guest/session reference, nonce, and constrained return URL. Verify server-to-server callbacks using configured secret references, timestamp bounds, constant-time signature comparison, replay storage, and idempotency. Reject callback decisions not bound to the originating guest/session. Add clear unavailable/expired/error behavior that leaves the guest unauthenticated.

**Deliverable:** External portal can authenticate a guest using a documented signed callback contract without access to network policy or arbitrary client addresses.

### Task 4: Payment-based authentication

Define a provider interface for checkout creation and webhook verification; add one adapter chosen during implementation for the operator's payment service. Checkout creation must bind provider metadata to a guest and plan. Verify webhook signature and event age, enforce idempotency, match payment amount/currency/plan/guest, and grant access only after successful confirmation. Ignore browser return as proof of payment. Keep provider secrets write-only and outside guest responses/logs.

**Deliverable:** Successful verified payment grants the selected access duration; all invalid or ambiguous payment states remain blocked.

### Task 5: Dashboard management, operations, and documentation

Add hotspot settings for enablement, branding, allowed methods, durations, external portal endpoint, payment provider/plans, and write-only secrets. Add voucher create/revoke and active-session list/revoke operations. Show runtime listener and session state without exposing credential material. Document TLS/DNS/AP behavior, supported captive detection, guest isolation limits, voucher lifecycle, external callback signing, payment webhook configuration, and rollback by disabling hotspot.

**Deliverable:** An operator can configure and administer the feature through the existing authenticated management UI and follow the setup guide.

## Validation sequence

- After each task, inspect the diff for unrelated changes and compare behavior with the acceptance criteria in the spec.
- Before final completion, review configuration compatibility, packet policy coverage, cookie/signature handling, and payment callback idempotency against the Review Focus list.
- Tests and automated verification are intentionally omitted from this plan unless the user asks for them.

## Open implementation choice

The spec deliberately leaves the payment provider pluggable. Task 4 cannot ship a concrete payment flow until an adapter is selected. If no preference is supplied at implementation start, choose a provider only if an existing project integration is found; otherwise implement the provider interface and webhook contract and leave provider configuration disabled until an adapter is selected.

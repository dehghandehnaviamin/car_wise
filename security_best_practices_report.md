# Security best-practices review

## Executive summary

The review covered the Go `net/http` API, Vue 3/Vite frontend, OpenAI integration, Nginx static server, and Docker Compose configuration. No authentication or user-account surface exists in this local MVP, and no raw HTML or shell execution path was found. The high-impact operational risks found in the initial implementation were fixed in this change: unbounded request/provider parsing, missing HTTP timeouts and browser headers, permissive CORS, hard-coded database fallback credentials, detailed internal error responses, and unvalidated external navigation.

## Findings and fixes

### SEC-001 — High — Unbounded HTTP and JSON work (fixed)

- Location: `cmd/api/main.go:164`, `cmd/api/main.go:347-377`, `cmd/api/main.go:1352-1364`, `cmd/api/main.go:1446`, `cmd/api/main.go:1505`.
- Evidence: the API accepts JSON POST requests and calls the OpenAI API.
- Impact: oversized request bodies or provider responses could consume memory or keep connections busy.
- Fix: explicit server read/write/idle/header timeouts; `http.MaxBytesReader` limits of 16 KiB for intent and 8 KiB for compare; strict single-value JSON decoding; 128 KiB bounded OpenAI response reads; 15-second OpenAI client timeout.
- Mitigation: Nginx also limits API bodies to 32 KiB and provider calls remain server-side.

### SEC-002 — Medium — CORS and browser hardening were too permissive (fixed)

- Location: `cmd/api/main.go:1393-1413`, `cmd/api/main.go:1415-1423`, `web/nginx.conf:6-10`.
- Evidence: the original middleware defaulted `Access-Control-Allow-Origin` to `*` and did not set security headers.
- Impact: arbitrary browser origins could read API responses if the service were exposed beyond the local demo; clickjacking and MIME-sniffing defenses were absent.
- Fix: CORS now allows only the configured origin and rejects mismatched `Origin` headers. API and Nginx responses set CSP, frame, MIME, referrer, and permissions policies.
- False-positive note: if a future deployment uses a different trusted frontend origin, set `CORS_ORIGIN` explicitly instead of using a wildcard.

### SEC-003 — High — Credential-bearing database fallback in source (fixed)

- Location: `cmd/api/main.go:121-125`, `docker-compose.yml:5-7,21`, `.env.example`.
- Evidence: the original API used a hard-coded local DSN fallback and Compose hard-coded the database password.
- Impact: copied source/config could silently reuse known credentials in a shared environment.
- Fix: `DATABASE_URL`, `POSTGRES_DB`, `POSTGRES_USER`, and `POSTGRES_PASSWORD` are required through environment configuration; `.env` remains ignored and the example contains only local-demo guidance.
- Mitigation: use a secret manager and TLS-enabled database connection for a real deployment.

### SEC-004 — Medium — Internal database/provider errors could leak to clients (fixed)

- Location: `cmd/api/main.go:299-340,391-399`.
- Evidence: handlers previously returned `err.Error()` through `http.Error`.
- Impact: SQL, host, schema, or provider details could be disclosed to a browser.
- Fix: request handlers now log the underlying error server-side and return a generic `internal server error` response.

### SEC-005 — Medium — External listing navigation needed an allowlist (fixed)

- Location: `web/src/App.vue:28-36,81`.
- Evidence: listing data controls `image_url` and `source_url` fields rendered by the browser.
- Impact: a poisoned snapshot could cause navigation to an unexpected scheme or host.
- Fix: the UI accepts HTTPS images only and allows source links only for the known public listing domains; links use `noopener noreferrer`.

### SEC-006 — Low — Third-party font is an explicit supply-chain dependency (accepted/documented)

- Location: `web/src/style.css:1`, `web/nginx.conf:10`.
- Evidence: Vazirmatn is loaded from Google Fonts.
- Impact: availability/privacy and third-party asset change risk.
- Decision: keep the font for Persian demo readability, restrict the CSP to the two Google Fonts origins, and document the dependency. A production deployment can self-host the font and remove those origins.

## Additional audit notes

- PostgreSQL access uses placeholders; no SQL string concatenation with user values was found.
- No `v-html`, `innerHTML`, runtime Vue template compilation, `eval`, `postMessage`, localStorage token, upload, or shell-execution path was found.
- There is no cookie-based authentication in this MVP, so CSRF/session controls are not currently applicable. If accounts are added, use HttpOnly/Secure/SameSite cookies plus CSRF protection.
- Go dependency vulnerability scanning (`govulncheck`) should be added to CI when the repository gains CI. The local environment did not provide the command during this review.

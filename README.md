# CarWise

Karwise is a production-shaped local MVP for an AI-assisted used-car buying workflow. It demonstrates the full vertical slice: real public listing snapshots, normalization, structured search, intent extraction, deterministic ranking, comparison, and source redirect.

## Quick start

```bash
cp .env.example .env
# Change POSTGRES_PASSWORD and the matching DATABASE_URL value for a shared machine.
# Add OPENAI_API_KEY for LLM intent extraction and comparison explanations (optional).
# Set OPENAI_BASE_URL to a compatible provider or proxy when needed.
docker compose up --build
```

Open <http://localhost:5173>. The API is available at <http://localhost:8080/api/v1/health>.

## Product decisions

- The demo uses real, source-linked snapshots so it is reproducible and does not depend on live HTML changes.
- Canonical storage is integer `price_irr`; the UI displays a human-friendly toman value.
- LLMs extract intent only. Hard constraints, retrieval, scoring, and explanations remain in application code.
- PostgreSQL is pgvector-ready. The first demo uses text retrieval for predictable startup; embeddings can be added without changing the domain model.
- Smart search is the default UI mode. A deterministic Persian parser remains authoritative for colors, origin, body condition, budget, model year, mileage, class, transmission, usage, economy, luxury, options, comfort, and fuel intent; the LLM cannot invent a model or relax a hard constraint.
- Compare accepts two or three real listing IDs only. The API returns every compared listing plus a per-car explanation, trade-offs, and best-fit use case. OpenAI provides the wording when configured; a deterministic fallback keeps the demo functional without an API key.
- OpenAI-compatible chat completions use OPENAI_BASE_URL (default: https://api.openai.com/v1), so a proxy or compatible provider can be configured without code changes.

## Real listing snapshots

The repository now includes a reproducible snapshot in `data/real_listings.json` with 264 normalized public Tehran offer records collected from current inventory pages:

- Hamrah Mechanic brand inventory pages provide structured `__NEXT_DATA__` records with source URL, price, mileage, model, year, image, and location.
- Divar public category pages provide a smaller JSON-LD snapshot adapter.
- Raw HTML responses are retained under `data/raw/` for parser re-runs and auditability.
- The demo keeps at most five best-covered models per requested brand and fifteen offers per model. Rare brands/models are kept at their actual available count; no records are fabricated.
- `FMC` currently has no public inventory page on Hamrah Mechanic, so it is reported as unavailable rather than synthesized.

Refresh the snapshot (respectful delay, no login or private API access):

```bash
python3 scripts/crawl_hamrah_mechanic.py --delay 1.0
python3 scripts/crawl_divar.py --pages 3 --delay 1.0
```

## API

- `GET /api/v1/listings?query=...&page=1&page_size=12`
- `POST /api/v1/assistant/search` with `{ "message": "...", "session_id": "..." }`
- `POST /api/v1/compare` with `{ "ids": [1, 3, 4] }`
- `GET /api/v1/health`

## Repository conventions

Source code, comments, logs, and technical documentation are English. Persian product copy is kept in the UI layer and seed data only.

## Security and operational notes

- The API uses bounded JSON bodies, strict JSON decoding, parameterized PostgreSQL queries, explicit CORS origin checks, response security headers, and HTTP read/write/idle timeouts.
- OpenAI calls use a 15-second client timeout and a bounded response body. API errors are logged server-side without returning database or provider details to the browser.
- Source links are allowlisted in the UI to the known public listing domains and open with `noopener noreferrer`. No user HTML is rendered with `v-html` or `innerHTML`.
- Docker Compose reads database credentials from `.env`; never commit a populated `.env` file. The included values are for a local demo only.
- Run the relevant checks before hand-off:

```bash
GOCACHE=/tmp/carwise-gocache GOPATH=/tmp/carwise-gopath GOMODCACHE=/tmp/carwise-gopath/pkg/mod go test ./...
cd web && npm run build
```

The snapshot is intentionally finite and source-linked. Missing fields such as engine-cylinder count or exact equipment packages are reported as uncertainty rather than fabricated facts.

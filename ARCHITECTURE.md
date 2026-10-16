# Architecture

CarWise is a modular monolith deliberately optimized for a small, explainable MVP.

## Runtime flow

1. The Vue client sends either a structured listing query or a natural-language assistant request.
2. The Go API normalizes and validates the request.
3. PostgreSQL applies hard filters before candidate scoring.
4. The ranking module scores price fit, user intent, mileage, body condition, freshness, market activity, and data confidence.
5. The retrieval module returns relevant vehicle knowledge snippets.
6. The API returns listings with source URLs and a deterministic explanation.

## Data boundaries

`source_records` and raw snapshots belong to ingestion. `listings` is the canonical read model. Knowledge chunks are separate from listing facts so recommendations never silently turn general vehicle knowledge into a listing claim.

## Extension points

- Add a `SourceAdapter` per permitted source.
- Add a scheduled snapshot worker without changing the search API.
- Add OpenAI embeddings to `knowledge_chunks.embedding` and replace text retrieval with pgvector similarity.
- Add user accounts after the anonymous local-session flow is validated.

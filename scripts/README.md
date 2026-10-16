# Data collection

`crawl_divar.py` collects public, server-rendered Tehran used-car listing pages from Divar. It stores each fetched HTML page under `data/raw/divar/` and writes normalized records to `data/real_listings.json`. Divar's `q` search URLs are excluded by its robots policy, so this adapter only uses category pagination.

`crawl_hamrah_mechanic.py` collects the public structured brand pages from Hamrah Mechanic, stores the raw HTML under `data/raw/hamrah-mechanic/`, and merges normalized offer snapshots into the same output file. The source currently exposes fewer than 10 offers for some models; the collector preserves the actual available count instead of fabricating records.

The collector is deliberately conservative: one request per second by default, no login, no private API calls, no contact data, and no challenge bypassing. It may produce fewer records for rare models; the output reports the actual coverage.

```bash
python3 scripts/crawl_divar.py --pages 3 --delay 1.0
python3 scripts/crawl_hamrah_mechanic.py --delay 1.0
```

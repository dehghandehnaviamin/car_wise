#!/usr/bin/env python3
"""Collect public Tehran used-car listing snapshots from Divar.

The crawler uses the server-rendered public listing pages and JSON-LD only.
It does not log in, solve challenges, call private APIs, or fetch contact data.
Run it slowly and keep the raw pages for reproducible parsing.
"""

from __future__ import annotations

import argparse
import json
import re
import time
from datetime import datetime, timezone
from pathlib import Path
from urllib.request import Request, urlopen

ROOT = Path(__file__).resolve().parents[1]
RAW_DIR = ROOT / "data" / "raw" / "divar"
OUT_FILE = ROOT / "data" / "real_listings.json"

MODELS: dict[str, dict[str, list[str]]] = {
    "ایران خودرو": {
        "پژو 206": ["206", "۲۰۶"], "پژو 207": ["207", "۲۰۷"], "دنا": ["دنا"],
        "تارا": ["تارا"], "سمند": ["سمند"],
    },
    "سایپا": {
        "پراید": ["پراید", "pride"], "تیبا": ["تیبا"], "ساینا": ["ساینا"],
        "شاهین": ["شاهین"], "کوییک": ["کوییک", "کوییک"],
    },
    "هیوندای": {"سوناتا": ["سوناتا", "sonata"], "النترا": ["النترا", "elantra"], "توسان": ["توسان", "tucson"], "سانتافه": ["سانتافه", "santa fe"], "i20": ["i20", "i۲۰"]},
    "کیا": {"سراتو": ["سراتو", "cerato"], "اپتیما": ["اپتیما", "optima"], "اسپورتیج": ["اسپورتیج", "sportage"], "سورنتو": ["سورنتو", "sorento"], "پیکانتو": ["پیکانتو", "picanto"]},
    "بنز": {"C200": ["c200", "c 200", "c کلاس"], "E200": ["e200", "e 200", "e کلاس"], "E250": ["e250", "e 250"], "S500": ["s500", "s 500"], "CLA": ["cla"]},
    "بی ام و": {"سری 3": ["سری ۳", "سری 3", "320", "325", "330"], "سری 5": ["سری ۵", "سری 5", "520", "528", "530"], "X3": ["x3"], "X4": ["x4"], "X5": ["x5"]},
    "تویوتا": {"کمری": ["کمری", "camry"], "کرولا": ["کرولا", "corolla"], "RAV4": ["راو۴", "راو ۴", "rav4"], "لندکروزر": ["لندکروزر", "land cruiser"], "یارिस": ["یاریس", "yaris"]},
    "نیسان": {"ماکسیما": ["ماکسیما", "maxima"], "تیانا": ["تیانا", "teana"], "ایکس تریل": ["ایکس تریل", "x-trail"], "مورانو": ["مورانو", "murano"], "جوک": ["جوک", "juke"]},
    "چری (MVM)": {"تیگو 5": ["تیگو ۵", "تیگو 5", "tiggo 5"], "تیگو 7": ["تیگو ۷", "تیگو 7", "tiggo 7"], "آریزو 5": ["آریزو ۵", "آریزو 5", "arrizo 5"], "آریزو 6": ["آریزو ۶", "آریزو 6", "arrizo 6"], "X22": ["x22", "x 22", "ایکس ۲۲"]},
    "هایما": {"S5": ["s5", "s 5", "هایما s5"], "S7": ["s7", "s 7", "هایما s7"], "8S": ["8s", "۸s", "هایما 8"], "7X": ["7x", "۷x"], "M3": ["m3", "m 3"]},
    "KMC": {"J7": ["j7", "j 7", "kmc j7"], "X5": ["x5", "x 5", "kmc x5"], "T8": ["t8", "t 8", "kmc t8"], "K7": ["k7", "k 7"], "A5": ["a5", "a 5"]},
    "FMC": {"SX5": ["sx5", "sx 5"], "T5": ["fmc t5", "فردا t5"], "511": ["فردا ۵۱۱", "فردا 511", "fmc 511"], "سوبا M4": ["سوبا", "m4"], "520": ["فردا ۵۲۰", "فردا 520"]},
    "JAC": {"S3": ["s3", "s 3", "جک s3"], "S5": ["s5", "s 5", "جک s5"], "J4": ["j4", "j 4", "جک j4"], "J7": ["j7", "j 7", "جک j7"], "T8": ["جک t8", "t8"]},
    "MG": {"3": ["mg3", "mg 3", "ام جی 3"], "360": ["mg360", "mg 360", "ام جی ۳۶۰"], "5": ["mg5", "mg 5", "ام جی 5"], "6": ["mg6", "mg 6", "ام جی 6"], "GS": ["mg gs", "ام جی gs"]},
    "BYD": {"S6": ["s6", "s 6", "byd s6"], "F3": ["f3", "f 3", "byd f3"], "Song": ["song", "سانگ"], "Qin": ["qin", "چین"], "Tang": ["tang", "تانگ"]},
}

BRAND_WORDS = {
    "ایران خودرو": ["ایران خودرو", "ایران‌خودرو", "peugeot", "پژو", "دنا", "تارا", "سمند"],
    "سایپا": ["سایپا", "saipa", "pride", "پراید", "تیبا", "ساینا", "شاهین", "کوییک"],
    "هیوندای": ["هیوندای", "hyundai"], "کیا": ["کیا", "kia"],
    "بنز": ["بنز", "مرسدس", "mercedes", "benz"], "بی ام و": ["بی ام و", "بی‌ام‌و", "bmw"],
    "تویوتا": ["تویوتا", "toyota"], "نیسان": ["نیسان", "nissan"],
    "چری (MVM)": ["چری", "mvm", "ام وی ام", "ام‌وی‌ام", "fownix", "فونیکس"],
    "هایما": ["هایما", "haima"], "KMC": ["kmc", "کرمان موتور"], "FMC": ["fmc", "فردا"],
    "JAC": ["jac", "جک"], "MG": ["mg", "ام جی"], "BYD": ["byd"],
}


def digits(value: str) -> str:
    table = str.maketrans("۰۱۲۳۴۵۶۷۸۹٠١٢٣٤٥٦٧٨٩", "01234567890123456789")
    return value.translate(table)


def clean(value: object) -> str:
    return str(value or "").strip()


def html_jsonld(html: str) -> list[dict]:
    blocks = re.findall(r'<script[^>]*type="application/ld\+json"[^>]*>(.*?)</script>', html, re.S | re.I)
    records: list[dict] = []
    for raw in blocks:
        try:
            obj = json.loads(raw.strip())
        except json.JSONDecodeError:
            continue
        values = obj if isinstance(obj, list) else [obj]
        records.extend(x for x in values if isinstance(x, dict) and x.get("@type") == "Car")
    return records


def classify(record: dict) -> tuple[str, str] | None:
    title = clean(record.get("name"))
    model = clean(record.get("model"))
    text = digits(f"{title} {model}").lower()
    for brand, models in MODELS.items():
        for canonical, aliases in models.items():
            if any(digits(alias).lower() in text for alias in aliases):
                return brand, canonical
    brand = clean((record.get("brand") or {}).get("name") if isinstance(record.get("brand"), dict) else record.get("brand"))
    for canonical, aliases in BRAND_WORDS.items():
        if any(alias.lower() in brand.lower() or alias.lower() in text for alias in aliases):
            return canonical, "نامشخص"
    return None


def parse_record(record: dict, fetched_at: str) -> dict | None:
    classified = classify(record)
    if not classified or classified[1] == "نامشخص":
        return None
    brand, model = classified
    offer = record.get("offers") or {}
    mileage = record.get("mileageFromOdometer") or {}
    info = record.get("web_info") or {}
    price = int(digits(clean(offer.get("price"))) or 0)
    if price <= 0:
        return None
    year = int(digits(clean(record.get("productionDate"))) or 0)
    km = int(digits(clean(mileage.get("value"))) or 0)
    url = clean(record.get("url"))
    if not url.startswith("https://divar.ir/"):
        return None
    damage = clean(record.get("knownVehicleDamages"))
    body = {"intact": "بدون رنگ", "some-paint": "رنگ‌شدگی محدود", "half-paint": "نیم‌رنگ", "full-paint": "تمام‌رنگ"}.get(damage, damage or "نامشخص")
    return {
        "brand": brand, "model": model, "trim": clean(record.get("name")), "year": year,
        "price_irr": price, "mileage_km": km, "color": clean(record.get("color")),
        "city": clean(info.get("city_persian")) or "تهران", "transmission": clean(record.get("vehicleTransmission")),
        "fuel_consumption_l100km": 0, "body_condition": body, "features": [],
        "image_url": clean(record.get("image")), "source": "divar", "source_url": url,
        "description": clean(record.get("name")), "published_at": "", "last_seen_at": fetched_at,
        "raw_source": "divar-jsonld",
    }


def fetch(url: str) -> bytes:
    request = Request(url, headers={"User-Agent": "CarWiseResearchBot/0.1 (local demo; respectful rate limit)"})
    with urlopen(request, timeout=30) as response:
        return response.read()


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--pages", type=int, default=3)
    parser.add_argument("--delay", type=float, default=1.0)
    args = parser.parse_args()
    RAW_DIR.mkdir(parents=True, exist_ok=True)
    collected: dict[str, dict] = {}
    now = datetime.now(timezone.utc).isoformat()
    for page in range(args.pages):
        url = "https://divar.ir/s/tehran/car" if page == 0 else f"https://divar.ir/s/tehran/car?page={page}"
        try:
            body = fetch(url)
            (RAW_DIR / f"page-{page:03d}.html").write_bytes(body)
            records = html_jsonld(body.decode("utf-8", "ignore"))
            for record in records:
                parsed = parse_record(record, now)
                if parsed:
                    collected[parsed["source_url"]] = parsed
            print(f"page={page} jsonld={len(records)} matched={len(collected)}", flush=True)
        except Exception as exc:
            print(f"page={page} error={exc}", flush=True)
        time.sleep(args.delay)
    output = list(collected.values())
    output.sort(key=lambda x: (x["brand"], x["model"], x["source_url"]))
    OUT_FILE.write_text(json.dumps(output, ensure_ascii=False, indent=2), encoding="utf-8")
    print(f"wrote {len(output)} listings to {OUT_FILE}")


if __name__ == "__main__":
    main()

#!/usr/bin/env python3
"""Collect public structured used-car offers from Hamrah Mechanic brand pages."""

from __future__ import annotations

import argparse
import json
import re
import time
from collections import Counter
from datetime import datetime, timezone
from pathlib import Path
from urllib.request import Request, urlopen

ROOT = Path(__file__).resolve().parents[1]
RAW_DIR = ROOT / "data" / "raw" / "hamrah-mechanic"
OUT_FILE = ROOT / "data" / "real_listings.json"

BRANDS = {
    "ایران خودرو": "irankhodro", "سایپا": "saipa", "هیوندای": "hyundai", "کیا": "kia",
    "بنز": "mercedesbenz", "بی ام و": "bmw", "تویوتا": "toyota", "نیسان": "nissan",
    "چری (MVM)": "mvm", "هایما": "haima", "KMC": "kmc", "FMC": "fmc",
    "JAC": "jac", "MG": "mg", "BYD": "byd",
}

MODEL_MAP = {
    "irankhodro": {"peugeot206": "پژو 206", "peugeot207": "پژو 207", "denaplus": "دنا", "soren": "سمند", "peugeotpars": "پژو پارس", "runna": "رونـا"},
    "saipa": {"saina": "ساینا", "quick": "کوییک", "atlas": "اطلس", "arios300": "آریو", "cs35saipa": "CS35", "cs35 plus": "CS35 Plus", "cs55 plus": "CS55 Plus", "dl5": "DL5"},
    "hyundai": {"elantra": "النترا", "sonatahybrid": "سوناتا", "sonata": "سوناتا", "santafe": "سانتافه", "tucson": "توسان", "i20": "i20", "accent": "اکسنت", "accentkermanmotor": "اکسنت", "kona": "کونا", "i10kermanmotor": "i10"},
    "kia": {"kiacerato": "سراتو", "cerato": "سراتو", "optima": "اپتیما", "sportage": "اسپورتیج", "cadenza": "کادنزا", "sorento": "سورنتو", "riohatchback": "ریو"},
    "mercedesbenz": {"aclass": "A کلاس", "cclass": "C کلاس"},
    "bmw": {"3series": "سری 3", "5series": "سری 5", "x3": "X3", "x2": "X2", "x1": "X1", "x4": "X4", "7series": "سری 7", "4seriesgrancoupe": "سری 4", "4seriesconvertible": "سری 4", "ix1": "iX1"},
    "toyota": {"camry": "کمری", "corolla": "کرولا", "corolla cross": "کرولا کراس", "chrhybrid": "C-HR", "frontlander": "فرانت‌لندر", "levin": "لوین", "prius": "پریوس", "rav4-hybrid": "RAV4", "venza": "ونزا"},
    "nissan": {"terra": "ترا", "juke": "جوک", "altima": "آلتیما", "kicks": "کی‌کس", "qashqai": "قشقایی", "teana": "تیانا"},
    "mvm": {"x55 pro": "X55 Pro", "mvmx22": "X22", "x33 cross": "X33 Cross", "mvmx33s": "X33S", "mvm315hatchback": "315", "mvm110": "110"},
    "haima": {"s7turbo": "S7", "s8": "S8", "haimas5": "S5", "7x": "7X"},
    "kmc": {"x5": "X5", "eagle": "Eagle", "a5": "A5", "k7": "K7", "j7 ev": "J7", "t8": "T8", "sr3": "SR3"},
    "jac": {"j4kermanmotor": "J4", "j7": "J7", "s5kermanmotor": "S5", "s3kermanmotor": "S3", "j5kermanmotor": "J5", "t8": "T8"},
    "mg": {"mggt": "GT", "mg550": "550", "mg3": "3", "mg6liftback": "6", "mg6magnet": "6", "5": "5", "mgrx5": "RX5", "mg360": "360"},
    "byd": {"song plus": "Song", "s6": "S6", "qinplus": "Qin", "destroyer05": "Destroyer 05", "qinl": "Qin", "sealion": "Sealion", "f3": "F3"},
}


def fetch(url: str) -> bytes:
    request = Request(url, headers={"User-Agent": "CarWiseResearchBot/0.1 (local demo; respectful rate limit)"})
    with urlopen(request, timeout=30) as response:
        return response.read()


def next_data(html: str) -> dict:
    match = re.search(r'<script id="__NEXT_DATA__" type="application/json">(.*?)</script>', html, re.S)
    if not match:
        raise ValueError("missing __NEXT_DATA__")
    return json.loads(match.group(1))


def normalize_car(car: dict, brand: str, slug: str, fetched_at: str) -> dict | None:
    model_slug = str(car.get("modelEnglishName", "")).lower()
    model = MODEL_MAP.get(slug, {}).get(model_slug)
    if not model:
        return None
    price = int(car.get("price") or 0)
    if price <= 0:
        return None
    km = int(car.get("km") or 0)
    if 0 < km < 1000:
        km *= 1000
    path = car.get("exhibitionDetailUrl") or ""
    return {
        "brand": brand, "model": model, "trim": car.get("carTypeName") or "", "year": int(car.get("carYear") or 0),
        "price_irr": price * 10, "mileage_km": km, "color": car.get("carColorName") or "",
        "city": car.get("carLocation") or "تهران", "transmission": car.get("gearBoxPersian") or "",
        "fuel_consumption_l100km": 0, "body_condition": "کارشناسی‌شده", "features": [x.get("title", "") for x in car.get("footerFeatures", []) if x.get("title")],
        "image_url": car.get("imageUrl") or "", "source": "hamrah-mechanic", "source_url": "https://www.hamrah-mechanic.com" + path,
        "description": f"{car.get('carNamePersian', '')} {car.get('carTypeName', '')}".strip(), "published_at": "", "last_seen_at": fetched_at,
        "raw_source": "hamrah-mechanic-next-data", "source_listing_id": str(car.get("orderId") or ""),
    }


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--delay", type=float, default=0.8)
    args = parser.parse_args()
    RAW_DIR.mkdir(parents=True, exist_ok=True)
    fetched_at = datetime.now(timezone.utc).isoformat()
    all_items: dict[str, dict] = {}
    for brand, slug in BRANDS.items():
        url = f"https://www.hamrah-mechanic.com/cars-for-sale/{slug}/"
        try:
            body = fetch(url)
            (RAW_DIR / f"{slug}.html").write_bytes(body)
            data = next_data(body.decode("utf-8", "ignore"))
            cars = data["props"]["pageProps"]["cars"]["list"]
            matched = 0
            for car in cars:
                item = normalize_car(car, brand, slug, fetched_at)
                if item:
                    all_items[item["source_url"]] = item
                    matched += 1
            print(f"brand={brand} source_cars={len(cars)} matched={matched} models={dict(Counter(x['model'] for x in all_items.values() if x['brand'] == brand))}", flush=True)
        except Exception as exc:
            print(f"brand={brand} error={exc}", flush=True)
        time.sleep(args.delay)

    existing = []
    if OUT_FILE.exists():
        try:
            existing = json.loads(OUT_FILE.read_text(encoding="utf-8"))
        except json.JSONDecodeError:
            existing = []
    merged = {x.get("source_url"): x for x in existing if x.get("source_url")}
    merged.update(all_items)
    # Keep the demo intentionally bounded: the five best-covered models per
    # requested brand, with at most fifteen current offers per model.
    grouped: dict[str, dict[str, list[dict]]] = {}
    for item in merged.values():
        grouped.setdefault(item.get("brand", ""), {}).setdefault(item.get("model", ""), []).append(item)
    limited: list[dict] = []
    for brand, models in grouped.items():
        selected_models = sorted(models, key=lambda name: (-len(models[name]), name))[:5]
        for model in selected_models:
            limited.extend(sorted(models[model], key=lambda x: x.get("last_seen_at", ""), reverse=True)[:15])
    output = sorted(limited, key=lambda x: (x.get("brand", ""), x.get("model", ""), x.get("source_url", "")))
    OUT_FILE.write_text(json.dumps(output, ensure_ascii=False, indent=2), encoding="utf-8")
    print(f"wrote {len(output)} total listings to {OUT_FILE}")


if __name__ == "__main__":
    main()

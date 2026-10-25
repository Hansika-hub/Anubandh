"""Regenerates parties.json, the shared seed fixtures (BUILD.md section 11).

Deterministic: the same seed always produces the same file. Run from the repo root:
    python -m simulators.fixtures.generate
"""

from __future__ import annotations

import json
import random
from pathlib import Path

OUT = Path(__file__).resolve().parent / "parties.json"

PREFIXES = [
    "Kerala",
    "Malabar",
    "Periyar",
    "Cochin",
    "Vembanad",
    "Sahyadri",
    "Kaveri",
    "Nila",
    "Anamudi",
    "Thekkady",
    "Coromandel",
    "Deccan",
    "Konkan",
    "Bharat",
    "Sree",
    "Lakshmi",
    "Ganga",
    "Vijaya",
    "Surya",
    "Chandra",
]
TRADES = {
    "MANUFACTURING": ["Forgings", "Castings", "Steel Works", "Precision Parts", "Fasteners", "Rubber Products", "Polymers"],
    "SERVICES": ["Logistics", "Testing Labs", "Tooling Services", "Engineering Services"],
    "TRADING": ["Traders", "Industrial Supplies", "Metals Trading"],
}
STATES = [("KL", "32"), ("TN", "33"), ("KA", "29"), ("MH", "27")]
CUSTOMER_NAMES = ["Auto", "Motors", "Mobility", "Tractors", "Two Wheelers", "Commercial Vehicles", "EV Systems"]


def gstin(rng: random.Random, state_code: str, idx: int) -> str:
    letters = "".join(rng.choice("ABCDEFGHIJKLMNOPQRSTUVWXYZ") for _ in range(5))
    return f"{state_code}{letters}{idx % 10000:04d}{rng.choice('ABCDEFGHJKLMNPQRSTUVWXYZ')}1Z{rng.choice('0123456789ABCDEFGHJK')}"


def supplier(rng: random.Random, i: int) -> dict:
    sid = f"SUP-{i:03d}"
    if i == 7:  # BUILD.md: SMALL, MANUFACTURING, 45-day written agreement
        category, activity, term, agreement = "SMALL", "MANUFACTURING", 45, True
    elif i == 12:  # BUILD.md: MICRO, 15-day
        category, activity, term, agreement = "MICRO", "MANUFACTURING", 15, False
    else:
        category = rng.choices(["MICRO", "SMALL", "MEDIUM", "NONE"], weights=[40, 30, 20, 10])[0]
        activity = "TRADING" if category == "NONE" else rng.choice(["MANUFACTURING", "MANUFACTURING", "SERVICES"])
        agreement = category in ("MICRO", "SMALL") and rng.random() < 0.5
        term = 45 if agreement else (15 if category in ("MICRO", "SMALL") else 30)
    state, code = ("KL", "32") if i in (7, 12) else rng.choice(STATES)
    district = 0 if i in (7, 12) else rng.randint(1, 14)
    msme = category in ("MICRO", "SMALL", "MEDIUM")
    return {
        "supplierId": sid,
        "name": f"{rng.choice(PREFIXES)} {rng.choice(TRADES[activity])} {'Pvt Ltd' if rng.random() < 0.6 else 'LLP'}",
        "mseCategory": category,
        "activity": activity,
        "udyamNo": f"UDYAM-{state}-{district:02d}-{i:07d}" if msme else None,
        "msmedApplies": category in ("MICRO", "SMALL"),
        "termDays": term,
        "writtenAgreement": agreement,
        "state": state,
        "gstin": gstin(rng, code, i),
        "accountRef": f"ACCT-{sid}",
        "meanDelayDays": round(rng.uniform(1, 12), 1),
        "qcRejectRate": round(rng.uniform(0.0, 0.12), 3),
    }


def main() -> None:
    rng = random.Random(20261020)
    names = {7: "Periyar Forgings Pvt Ltd", 12: "Nila Precision Parts"}
    suppliers = [supplier(rng, i) for i in range(1, 51)]
    for s in suppliers:
        n = int(s["supplierId"][4:])
        if n in names:
            s["name"] = names[n]
    customers = []
    for i in range(1, 31):
        state, code = rng.choice(STATES)
        customers.append(
            {
                "customerId": f"CUS-{i:03d}",
                "name": f"{rng.choice(PREFIXES)} {rng.choice(CUSTOMER_NAMES)} Ltd",
                "state": state,
                "gstin": gstin(rng, code, 500 + i),
                "creditDays": rng.choice([30, 45, 60, 90]),
            }
        )
    data = {
        "buyers": [
            {
                "buyerId": "BUYER-001",
                "name": "Malabar Auto Components Ltd",
                "state": "KL",
                "gstin": "32AABCM4411K1Z7",
                "accountRef": "ACCT-BUYER-001",
            }
        ],
        "suppliers": suppliers,
        "customers": customers,
        "demoDeals": [
            {
                "dealId": "DL-20261020-0001",
                "poNumber": "PO-4411",
                "buyerId": "BUYER-001",
                "supplierId": "SUP-007",
                "lines": [{"lineNo": 1, "sku": "SKU-STEEL-12", "orderedQty": 100, "unitPricePaise": 1000000}],
                "lienAmountPaise": 100000000,
                "invoiceIrn": "IRN-PO-4411-0001",
                "note": "Rs 10L demo deal: GRN 100, QC 80/20, Rs 8L released, Rs 2L held, 20 replacements",
            },
            {
                "dealId": "DL-20261020-0002",
                "poNumber": "PO-4412",
                "buyerId": "BUYER-001",
                "supplierId": "SUP-012",
                "lines": [{"lineNo": 1, "sku": "SKU-STEEL-16", "orderedQty": 60, "unitPricePaise": 10000000}],
                "lienAmountPaise": 600000000,
                "invoiceIrn": "IRN-PO-4412-0001",
                "note": "Rs 60L MICRO deal: deemed acceptance after 15 days, needs PLANT_HEAD + CFO",
            },
        ],
    }
    OUT.write_text(json.dumps(data, indent=2, ensure_ascii=False) + "\n", encoding="utf-8", newline="\n")
    print(f"wrote {OUT} ({len(suppliers)} suppliers, {len(customers)} customers)")


if __name__ == "__main__":
    main()

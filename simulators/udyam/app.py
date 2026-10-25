"""sim-udyam (:8104): Udyam (MSME) registration lookup.

Contract (BUILD.md section 11):
  GET /udyam/v1/{udyamNo} -> {udyamNo, category: MICRO|SMALL|MEDIUM, activity, valid: true}

Registrations come from fixtures/parties.json (SUP-001..SUP-050). An unknown but well-formed number
is a 404; a malformed one is a 400. GET /udyam/v1/by-supplier/{supplierId} is a demo helper.
"""

from __future__ import annotations

import re

from simulators.common import SimError, new_app, suppliers

UDYAM_RE = re.compile(r"^UDYAM-[A-Z]{2}-\d{2}-\d{7}$")


def registration(s: dict) -> dict:
    return {
        "udyamNo": s["udyamNo"],
        "category": s["mseCategory"],
        "activity": s["activity"],
        "valid": True,
        "enterpriseName": s["name"],
        "supplierId": s["supplierId"],
        "state": s["state"],
        "msmedApplies": s["msmedApplies"],
    }


def create_app():
    app = new_app("sim-udyam", "Anubandh sim-udyam")
    by_no = {s["udyamNo"]: s for s in suppliers().values() if s["udyamNo"]}

    @app.get("/udyam/v1/by-supplier/{supplier_id}")
    def by_supplier(supplier_id: str) -> dict:
        s = suppliers().get(supplier_id)
        if s is None:
            raise SimError(404, "ERR_NOT_FOUND", f"no supplier {supplier_id}")
        if not s["udyamNo"]:
            raise SimError(404, "ERR_NOT_FOUND", f"{supplier_id} has no Udyam registration (category NONE)")
        return registration(s)

    @app.get("/udyam/v1/{udyam_no}")
    def get_registration(udyam_no: str) -> dict:
        if not UDYAM_RE.match(udyam_no):
            raise SimError(400, "ERR_BAD_REQUEST", "udyamNo must look like UDYAM-XX-00-0000000")
        s = by_no.get(udyam_no)
        if s is None:
            raise SimError(404, "ERR_NOT_FOUND", f"no Udyam registration {udyam_no}")
        return registration(s)

    return app


app = create_app()

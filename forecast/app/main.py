from __future__ import annotations

from fastapi import FastAPI, Request
from fastapi.exceptions import RequestValidationError
from fastapi.responses import JSONResponse

from forecast.app.routes import router

app = FastAPI(title="Anubandh Forecast", version="0.1.0")
app.include_router(router)


@app.get("/health")
def health() -> dict:
    return {"status": "ok", "svc": "forecast"}


@app.exception_handler(RequestValidationError)
def validation_error(_: Request, exc: RequestValidationError) -> JSONResponse:
    return JSONResponse(status_code=400, content={"error": {"code": "ERR_BAD_REQUEST", "message": "Invalid request", "details": {"errors": [str(e) for e in exc.errors()]}}})

# One image for all four simulators. SIM selects the app: bank | grn | gst | udyam.
# Build context = repo root:  docker build -f simulators/Dockerfile -t anubandh/simulators .
FROM python:3.12-slim
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1
WORKDIR /code
COPY simulators/requirements.txt /tmp/requirements.txt
RUN pip install --no-cache-dir -r /tmp/requirements.txt \
 && useradd --system --uid 10001 sim
COPY simulators /code/simulators
USER sim
ENV SIM=bank PORT=8101
HEALTHCHECK --interval=10s --timeout=3s --retries=5 \
  CMD python -c "import os,urllib.request; urllib.request.urlopen('http://127.0.0.1:%s/health' % os.environ['PORT'], timeout=2)"
CMD ["sh", "-c", "exec uvicorn simulators.${SIM}.app:app --host 0.0.0.0 --port ${PORT}"]

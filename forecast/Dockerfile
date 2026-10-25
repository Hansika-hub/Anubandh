FROM python:3.11-slim
WORKDIR /code
# LightGBM loads libgomp (OpenMP) at import time; the slim image does not ship it.
RUN apt-get update && apt-get install -y --no-install-recommends libgomp1 && rm -rf /var/lib/apt/lists/*
COPY forecast/requirements.txt /tmp/requirements.txt
RUN pip install --no-cache-dir -r /tmp/requirements.txt
COPY forecast /code/forecast
EXPOSE 8090
CMD ["uvicorn", "forecast.app.main:app", "--host", "0.0.0.0", "--port", "8090"]

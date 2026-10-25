FROM python:3.11-slim
WORKDIR /code
COPY forecast/requirements.txt /tmp/requirements.txt
RUN pip install --no-cache-dir -r /tmp/requirements.txt
COPY forecast /code/forecast
EXPOSE 8090
CMD ["uvicorn", "forecast.app.main:app", "--host", "0.0.0.0", "--port", "8090"]

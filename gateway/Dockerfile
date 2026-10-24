# Anubandh gateway (M2). Build context = repository root, so the image carries db/migrations:
#   docker build -f gateway/Dockerfile -t anubandh/gateway .
# docker-compose (M3):  build: { context: .., dockerfile: gateway/Dockerfile }
FROM golang:1.26 AS build
WORKDIR /src/gateway
COPY gateway/go.mod gateway/go.sum ./
RUN go mod download
COPY gateway/ ./
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/gateway ./cmd/gateway \
 && mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/gateway /app/gateway
COPY --from=build --chown=nonroot:nonroot /out/data /data
COPY gateway/config/users.json /app/config/users.json
COPY db/migrations /app/migrations
ENV GATEWAY_ADDR=:8080 \
    GATEWAY_USERS_FILE=/app/config/users.json \
    MIGRATIONS_DIR=/app/migrations \
    FABRIC_MOCK_STATE=/data/mock-ledger.json \
    FABRIC_CHANNEL=anubandh-ch \
    FABRIC_CC_NAME=deal
EXPOSE 8080
USER nonroot:nonroot
HEALTHCHECK --interval=10s --timeout=4s --start-period=20s --retries=3 CMD ["/app/gateway", "-healthcheck"]
ENTRYPOINT ["/app/gateway"]

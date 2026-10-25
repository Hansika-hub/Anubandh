# Builds ingest-api and ingest-worker WITH the Kafka/Postgres adapters (-tags adapters).
# The default `go build` (no tag) is stdlib-only and is what the unit/burst tests use.
#
# go.mod/go.sum in this repo list no third-party modules yet (they could not be resolved when the
# code was written). This build resolves them. To make builds reproducible, run `make ingest-tidy`
# once on a networked machine and commit the resulting ingest/go.mod + go.sum.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY . .
RUN go get github.com/twmb/franz-go@v1.18.1 github.com/jackc/pgx/v5@v5.7.2 \
 && go mod tidy \
 && CGO_ENABLED=0 go build -tags adapters -trimpath -ldflags="-s -w" -o /out/ingest-api ./api \
 && CGO_ENABLED=0 go build -tags adapters -trimpath -ldflags="-s -w" -o /out/ingest-worker ./worker

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/ingest-api /out/ingest-worker /app/
USER nonroot:nonroot
EXPOSE 8081
ENTRYPOINT ["/app/ingest-api"]

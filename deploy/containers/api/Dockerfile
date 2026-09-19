FROM golang:1.27.1-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
RUN GOBIN=/out CGO_ENABLED=0 go install github.com/pressly/goose/v3/cmd/goose@v3.24.3
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/api ./cmd/api \
    && CGO_ENABLED=0 GOOS=linux go build -o /out/devseed ./cmd/devseed

FROM debian:bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates wget \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --system --uid 10001 --home /app sandbox
WORKDIR /app
COPY --from=build /out/api /out/devseed /out/goose /app/
COPY migrations /app/migrations
RUN chmod 755 /app/api /app/devseed /app/goose
USER sandbox
EXPOSE 8080
ENTRYPOINT ["/bin/sh", "-c", "/app/goose -dir /app/migrations postgres \"$DATABASE_URL\" up && exec /app/api"]

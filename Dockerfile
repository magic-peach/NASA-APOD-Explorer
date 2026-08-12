# syntax=docker/dockerfile:1

# ---- Build stage: compile a static binary ----------------------------------
FROM golang:1.24-alpine AS build
WORKDIR /src

# Download modules first so this layer is cached until go.mod/go.sum change.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# CGO_ENABLED=0 gives a fully static binary. That works because the SQLite
# driver (modernc.org/sqlite) is pure Go, so no C toolchain or libc is needed
# at runtime. -trimpath and -s -w drop local paths and debug symbols.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

# The runtime image has no shell, so create the data directory here, owned
# by the distroless "nonroot" user (uid 65532), and copy it across.
RUN mkdir -p /out/data && chown 65532:65532 /out/data

# ---- Runtime stage: distroless, non-root -----------------------------------
# No shell, package manager or other binaries: a much smaller attack surface.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/server /app/server
COPY --from=build --chown=65532:65532 /out/data /data

ENV PORT=8080 \
    DB_PATH=/data/app.db \
    LOG_FORMAT=json
EXPOSE 8080
VOLUME ["/data"]
USER nonroot:nonroot

# The binary checks its own /healthz (there's no curl in distroless).
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/app/server", "healthcheck"]

ENTRYPOINT ["/app/server"]

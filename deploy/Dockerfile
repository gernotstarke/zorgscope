# syntax=docker/dockerfile:1
ARG GO_IMAGE=golang:1.26
FROM ${GO_IMAGE} AS build
ARG CMD=zorgscope
WORKDIR /src
COPY go.mod go.sum* ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/app ./cmd/${CMD}
# gcr.io/distroless/static-debian12:nonroot runs as uid:gid 65532:65532 and has no shell, so a named
# volume mounted at /data (which Docker otherwise creates root-owned) would leave the SQLite store
# unable to open its file (SQLITE_CANTOPEN). Pre-create /data with nonroot ownership here — Docker
# copies a mount point's pre-existing content and permissions into a new named volume on first use,
# so the volume inherits this ownership instead of the default root:root.
RUN mkdir -p /out/data && chown 65532:65532 /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/app /app/bin
COPY config /app/config
COPY --from=build --chown=65532:65532 /out/data /data
ENV ZORGSCOPE_CONFIG=/app/config/zorgscope.yaml ZORGSCOPE_DATA=/data/zorgscope.db PORT=8080
EXPOSE 8080
VOLUME ["/data"]
USER nonroot
ENTRYPOINT ["/app/bin"]

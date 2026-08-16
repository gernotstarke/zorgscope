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

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/app /app/bin
COPY config /app/config
ENV ZORGSCOPE_CONFIG=/app/config/zorgscope.yaml ZORGSCOPE_DATA=/data/zorgscope.db PORT=8080
EXPOSE 8080
VOLUME ["/data"]
USER nonroot
ENTRYPOINT ["/app/bin"]

# Static, non-root, scratch-family image, kept small so the cold start after auto-stop stays
# cheap enough for an interactive page.
FROM golang:1.26 AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/zorgscope ./cmd/zorgscope

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/zorgscope /zorgscope
COPY --from=build /src/config/zorgscope.yaml /config/zorgscope.yaml

USER nonroot:nonroot
ENV PORT=8080 CONFIG_PATH=/config/zorgscope.yaml
EXPOSE 8080

ENTRYPOINT ["/zorgscope"]

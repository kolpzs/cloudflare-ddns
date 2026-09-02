FROM golang:1.24-alpine AS build

WORKDIR /src

COPY go.mod ./
RUN go mod download

COPY main.go ./

RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath -ldflags="-s -w" \
    -o /out/ddns .

RUN mkdir -p /out/data && touch /out/data/.keep

FROM scratch

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build --chown=65532:65532 /out/ddns /ddns
COPY --from=build --chown=65532:65532 /out/data /data

USER 65532:65532

HEALTHCHECK --interval=5m --timeout=15s --start-period=30s --retries=3 \
    CMD ["/ddns", "-healthcheck"]

ENTRYPOINT ["/ddns"]

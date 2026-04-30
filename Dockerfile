FROM golang:1.26.1-alpine AS builder
WORKDIR /src

RUN apk add --no-cache ca-certificates

ARG TARGETOS=linux
ARG TARGETARCH=amd64

COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal

RUN CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
  go build -trimpath -ldflags="-s -w" -o /out/llm-proxy-lite ./cmd/llm-proxy-lite

FROM scratch AS runtime
WORKDIR /app

ENV PORT=3000
ENV HOST=0.0.0.0
ENV LOG_LEVEL=info

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /out/llm-proxy-lite /usr/local/bin/llm-proxy-lite

USER 65532:65532

EXPOSE 3000

ENTRYPOINT ["/usr/local/bin/llm-proxy-lite"]

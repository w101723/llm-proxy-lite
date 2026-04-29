FROM node:20-alpine AS builder
WORKDIR /app

RUN apk add --no-cache make

ARG TARGETOS=linux
ARG TARGETARCH=amd64

COPY package.json package-lock.json ./
RUN npm ci

COPY Makefile ./
COPY scripts ./scripts
COPY llm-proxy-lite.js ./

RUN set -eux; \
  case "$TARGETARCH" in \
    amd64) NODE_ARCH=x64 ;; \
    arm64) NODE_ARCH=arm64 ;; \
    *) echo "Unsupported TARGETARCH: $TARGETARCH" >&2; exit 1 ;; \
  esac; \
  make build-bin PLATFORM="$TARGETOS" ARCH="$NODE_ARCH"; \
  mkdir -p /out; \
  cp "dist/${TARGETOS}-${NODE_ARCH}/llm-proxy-lite-${TARGETOS}-${NODE_ARCH}" /out/llm-proxy-lite; \
  chmod +x /out/llm-proxy-lite

FROM node:20-alpine AS runtime
WORKDIR /app

ENV PORT=3000
ENV HOST=0.0.0.0
ENV LOG_LEVEL=info

COPY --from=builder /out/llm-proxy-lite /usr/local/bin/llm-proxy-lite

EXPOSE 3000

CMD ["/usr/local/bin/llm-proxy-lite"]

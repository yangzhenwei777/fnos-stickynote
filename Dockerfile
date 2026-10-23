# Stage 1: Build frontend
FROM node:22.23.4-alpine AS web-build
WORKDIR /app/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# Stage 2: Build backend
# 钉版纪律：与工具链一致（CI setup-go 1.26.10），禁 float 小版本
FROM golang:1.26.10-alpine AS server-build
RUN apk add --no-cache git
WORKDIR /app/server
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server/ ./
COPY --from=web-build /app/web/dist ./static/dist
ARG VERSION=dev
ARG BUILD_TIME=unknown
ARG GIT_COMMIT=unknown
RUN CGO_ENABLED=0 go build -ldflags "-s -w -X main.Version=${VERSION} -X main.BuildTime=${BUILD_TIME} -X main.GitCommit=${GIT_COMMIT}" -o /sticky-note ./main.go

# Stage 3: Runtime
FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata
RUN adduser -D -u 1000 sticky-note
WORKDIR /app
COPY --from=server-build /sticky-note /app/sticky-note
RUN chown -R sticky-note:sticky-note /app
# OCI 标准标签（docker inspect / Docker Hub 详情页可见；值由 CI build-args 注入）
ARG VERSION=dev
ARG BUILD_TIME=unknown
ARG GIT_COMMIT=unknown
LABEL org.opencontainers.image.title="StickyNote" \
      org.opencontainers.image.description="Lightweight multi-user note app (fnOS compatible, TCP mode in container)" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.source="https://github.com/yangzhenwei777/fnos-stickynote" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.revision="${GIT_COMMIT}" \
      org.opencontainers.image.created="${BUILD_TIME}"
EXPOSE 13389
VOLUME ["/app/data"]
# 容器内无 TRIM_APPDEST → unix socket 监听禁用（server/cmd/socket.go DefaultSocketPath 空串语义），
# 纯 TCP 双轨直连模式即为容器专用形态；HEALTHCHECK 用 busybox wget（alpine 自带）
ENV PORT=13389 DATA_DIR=/app/data
HEALTHCHECK --interval=30s --timeout=3s CMD wget -q --spider http://localhost:13389/api/health || exit 1
USER sticky-note
ENTRYPOINT ["/app/sticky-note"]
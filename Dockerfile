# syntax=docker/dockerfile:1

# Build stage. The zarf submodule must be present at ./zarf
# (git submodule update --init --recursive).
FROM golang:1.26-alpine AS build
WORKDIR /workspace

# Copy the vendored zarf submodule first so the replace directive resolves.
COPY zarf/ ./zarf/

COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ ./cmd/
COPY internal/ ./internal/

ARG VERSION=0.1.0
RUN CGO_ENABLED=0 GOOS=linux go build -buildvcs=false \
      -ldflags="-s -w -X main.version=${VERSION}" \
      -o /out/zarf-api ./cmd/zarf-api

# Runtime stage. Alpine (not scratch/distroless) on purpose: zarf component
# actions execute shell scripts during deploy/remove, which needs /bin/sh.
FROM alpine:3.22
RUN apk add --no-cache ca-certificates && \
    addgroup -S -g 10001 zarf-api && adduser -S -u 10001 -G zarf-api zarf-api

COPY --from=build /out/zarf-api /usr/local/bin/zarf-api

ENV ZARF_API_DATA_DIR=/data \
    ZARF_API_PORT=8080 \
    ZARF_API_LOG_FORMAT=json

RUN mkdir -p /data && chown -R zarf-api:zarf-api /data
USER zarf-api
VOLUME ["/data"]
EXPOSE 8080

ENTRYPOINT ["zarf-api"]

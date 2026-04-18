# syntax=docker/dockerfile:1.7

# ---------------------------------------------------------------------------
# Build stage
# ---------------------------------------------------------------------------
FROM golang:1.23 AS builder

ARG VERSION=dev
WORKDIR /workspace

# Cache deps separately from source to keep rebuilds fast.
COPY go.mod go.sum* ./
RUN go mod download

COPY cmd/ cmd/
COPY api/ api/
COPY internal/ internal/

# Fully static, stripped binary for the distroless runtime image.
RUN CGO_ENABLED=0 GOOS=linux go build \
      -trimpath \
      -ldflags="-s -w -X 'main.buildVersion=${VERSION}'" \
      -o /out/manager ./cmd/manager

# ---------------------------------------------------------------------------
# Runtime stage — distroless/static nonroot (lowest possible footprint)
# ---------------------------------------------------------------------------
FROM gcr.io/distroless/static:nonroot

WORKDIR /
COPY --from=builder /out/manager /manager

USER 65532:65532
ENTRYPOINT ["/manager"]

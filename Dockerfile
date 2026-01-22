FROM golang:1.24-alpine AS builder

WORKDIR /app
RUN apk add --no-cache ca-certificates

# Cache dependencies
COPY go.mod go.sum ./
RUN go mod download


# Copy source
COPY main.go ./

# Use Buildx platform args
ARG TARGETOS
ARG TARGETARCH
ENV CGO_ENABLED=0
ENV GOOS=$TARGETOS
ENV GOARCH=$TARGETARCH

RUN go build -trimpath -ldflags="-s -w" -o gotigram

# --- Distroless runtime image ---

# Runtime stage
FROM gcr.io/distroless/base-debian13

WORKDIR /app
COPY --from=builder /app/gotigram /app/gotigram

ENTRYPOINT ["/app/gotigram"]

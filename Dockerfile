FROM golang:1.21-alpine AS build

RUN apk add --no-cache git

WORKDIR /app

COPY go.mod go.sum ./

RUN go mod download

COPY main.go ./

RUN go build -o gotigram main.go

# --- Minimal runtime image ---

FROM alpine:latest

WORKDIR /app

COPY --from=build /app/gotigram .

CMD ["./gotigram"]

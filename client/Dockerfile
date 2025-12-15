FROM golang:1.22-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY client ./client

RUN CGO_ENABLED=0 GOOS=linux go build -o /client ./client

FROM alpine:3.20
COPY --from=builder /client /client
ENTRYPOINT ["/client"]


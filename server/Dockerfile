FROM golang:1.22-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY server ./server

RUN CGO_ENABLED=0 GOOS=linux go build -o /server ./server

FROM alpine:3.20
RUN mkdir -p /var/log/app
COPY --from=builder /server /server
EXPOSE 8080
CMD ["/server"]


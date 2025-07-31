# Build stage
FROM golang:1.22.0-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -v -o /server

# Final stage
FROM scratch

WORKDIR /

COPY --from=builder /server /server

EXPOSE 8080

USER 10001

CMD ["/server"]

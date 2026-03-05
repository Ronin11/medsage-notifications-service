FROM golang:1.24-alpine AS builder
RUN apk add --no-cache git
WORKDIR /app
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /notifications-service .

FROM alpine:3.19
RUN apk --no-cache add ca-certificates
COPY --from=builder /notifications-service .
RUN adduser -D -g '' appuser
USER appuser
EXPOSE 8080
CMD ["./notifications-service"]

FROM golang:1.26-alpine AS builder
RUN apk add --no-cache git
WORKDIR /app
COPY proto/gen/go/ /proto/gen/go/
COPY notifications-service/go.mod notifications-service/go.sum* ./
RUN go mod download
COPY notifications-service/ .
RUN CGO_ENABLED=0 GOOS=linux go build -o /notifications-service .

FROM alpine:3.19
RUN apk --no-cache add ca-certificates
COPY --from=builder /notifications-service .
RUN adduser -D -g '' appuser
USER appuser
EXPOSE 8080
CMD ["./notifications-service"]

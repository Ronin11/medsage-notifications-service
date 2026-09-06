FROM golang:1.26-alpine AS builder
RUN apk add --no-cache git
WORKDIR /app
COPY notifications-service/go.mod notifications-service/go.sum* ./
# The shared modules (medsage-proto, medsage-authkit) are private, so this
# build needs a credential to fetch them. It arrives as a BuildKit secret, is
# used only for this layer, and never lands in the image — unlike a build ARG,
# which would be visible in the history.
RUN --mount=type=secret,id=modules_token \
    GOPRIVATE='github.com/Ronin11/*' \
    sh -c 'test -s /run/secrets/modules_token || { echo; echo "ERROR: MODULES_TOKEN is empty or unset."; echo "Set it in .env — a read-only token for the private module repos."; echo "See .env.example."; echo; exit 1; }; \
           git config --global url."https://x-access-token:$(cat /run/secrets/modules_token)@github.com/".insteadOf "https://github.com/" && \
           go mod download && \
           git config --global --unset-all url."https://x-access-token:$(cat /run/secrets/modules_token)@github.com/".insteadOf'
COPY notifications-service/ .
RUN CGO_ENABLED=0 GOOS=linux go build -o /notifications-service .

FROM alpine:3.19
RUN apk --no-cache add ca-certificates
COPY --from=builder /notifications-service .
RUN adduser -D -g '' appuser
USER appuser
EXPOSE 8080
CMD ["./notifications-service"]

# Build stage
FROM golang:1.21-alpine AS builder
RUN apk add --no-cache git
WORKDIR /src
COPY . .
RUN go build -o /bin/ingest ./

# Runtime
FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=builder /bin/ingest /bin/ingest
EXPOSE 8080
ENTRYPOINT ["/bin/ingest"]

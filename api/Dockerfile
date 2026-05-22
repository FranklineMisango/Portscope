# Build stage
FROM golang:1.21-alpine AS builder
RUN apk add --no-cache git
WORKDIR /src
COPY . .
RUN go mod download
RUN go build -o /bin/api ./

# Runtime

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=builder /bin/api /bin/api
EXPOSE 8080
ENTRYPOINT ["/bin/api"]

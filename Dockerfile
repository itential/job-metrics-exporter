FROM golang:1.22-alpine AS builder

RUN apk add --no-cache ca-certificates

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
RUN CGO_ENABLED=0 go build -ldflags "-s -w -X main.version=${VERSION}" \
    -o itential-job-metrics-exporter ./cmd/exporter

FROM scratch

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /build/itential-job-metrics-exporter /itential-job-metrics-exporter

EXPOSE 9477

ENTRYPOINT ["/itential-job-metrics-exporter"]

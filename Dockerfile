FROM golang:1.24-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 go build -ldflags="-w -s" -o /container-md .

FROM alpine:latest

RUN apk --no-cache add ca-certificates

COPY --from=builder /container-md /container-md

ENTRYPOINT ["/container-md"]
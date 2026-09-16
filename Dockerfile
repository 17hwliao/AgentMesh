FROM golang:1.26.3-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -buildvcs=false -trimpath -ldflags='-s -w' -o /out/api ./cmd/api \
    && CGO_ENABLED=0 GOOS=linux go build -buildvcs=false -trimpath -ldflags='-s -w' -o /out/usage-kafka-relay ./cmd/usage-kafka-relay \
    && CGO_ENABLED=0 GOOS=linux go build -buildvcs=false -trimpath -ldflags='-s -w' -o /out/usage-kafka-worker ./cmd/usage-kafka-worker

FROM alpine:3.21
RUN apk add --no-cache ca-certificates wget
RUN adduser -D -H -u 10001 appuser
COPY --from=build /out/api /usr/local/bin/api
COPY --from=build /out/usage-kafka-relay /usr/local/bin/usage-kafka-relay
COPY --from=build /out/usage-kafka-worker /usr/local/bin/usage-kafka-worker
USER appuser

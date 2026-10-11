ARG GO_BUILD_IMAGE=golang:1.26.9-alpine
FROM ${GO_BUILD_IMAGE} AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -buildvcs=false -trimpath -ldflags='-s -w' -o /out/api ./cmd/api \
    && CGO_ENABLED=0 GOOS=linux go build -buildvcs=false -trimpath -ldflags='-s -w' -o /out/usage-kafka-relay ./cmd/usage-kafka-relay \
    && CGO_ENABLED=0 GOOS=linux go build -buildvcs=false -trimpath -ldflags='-s -w' -o /out/usage-kafka-worker ./cmd/usage-kafka-worker \
    && CGO_ENABLED=0 GOOS=linux go build -buildvcs=false -trimpath -ldflags='-s -w' -o /out/migrate ./cmd/migrate \
    && CGO_ENABLED=0 GOOS=linux go build -buildvcs=false -trimpath -ldflags='-s -w' -o /out/quota-rebuild ./cmd/quota-rebuild \
    && CGO_ENABLED=0 GOOS=linux go build -buildvcs=false -trimpath -ldflags='-s -w' -o /out/model-runner ./cmd/model-runner \
    && CGO_ENABLED=0 GOOS=linux go build -buildvcs=false -trimpath -ldflags='-s -w' -o /out/grpc-probe ./cmd/grpc-probe

FROM build AS verify
RUN apk add --no-cache build-base
RUN CGO_ENABLED=1 go test -race -count=1 ./... && go vet ./...

FROM alpine:3.21
RUN apk add --no-cache ca-certificates wget
RUN adduser -D -H -u 10001 appuser
COPY --from=build /out/ /usr/local/bin/
USER appuser

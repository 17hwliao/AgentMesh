ARG GO_BUILD_IMAGE=golang:1.26.9-alpine
FROM ${GO_BUILD_IMAGE} AS build
WORKDIR /proxy-src
COPY proxybuild/go.mod proxybuild/go.sum ./
RUN go mod download
COPY proxybuild/main.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -buildvcs=false -trimpath -o /out/caddy .
# Keep the official runtime layout while replacing its vulnerable Go binary.
FROM caddy:2.11.7-alpine@sha256:d8542f48d34a9cf4e4c11a478865229840e87e4c96ea3f439101f31a5d35f75f
COPY --from=build /out/caddy /usr/bin/caddy

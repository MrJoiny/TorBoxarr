FROM --platform=$BUILDPLATFORM golang:1.26.1 AS build
WORKDIR /app
ARG TARGETOS
ARG TARGETARCH
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod     --mount=type=cache,target=/root/.cache/go-build     go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN --mount=type=cache,target=/go/pkg/mod     --mount=type=cache,target=/root/.cache/go-build     CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /out/torboxarr ./cmd/torboxarr

FROM alpine:3.23.3
RUN apk add --no-cache ca-certificates su-exec
WORKDIR /app
COPY --from=build /out/torboxarr /app/torboxarr
COPY deploy/entrypoint.sh /entrypoint.sh
RUN chmod 0755 /entrypoint.sh
EXPOSE 8085
ENTRYPOINT ["/entrypoint.sh"]

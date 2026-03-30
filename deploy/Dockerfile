FROM golang:1.26.1 AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod     --mount=type=cache,target=/root/.cache/go-build     go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN --mount=type=cache,target=/go/pkg/mod     --mount=type=cache,target=/root/.cache/go-build     CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/torboxarr ./cmd/torboxarr
RUN mkdir -p /out/var/lib/torboxarr

FROM gcr.io/distroless/static-debian12
WORKDIR /app
COPY --from=build /out/torboxarr /app/torboxarr
COPY --from=build --chown=65532:65532 /out/var/lib/torboxarr /var/lib/torboxarr
USER 65532:65532
EXPOSE 8085
ENTRYPOINT ["/app/torboxarr"]

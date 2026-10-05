FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETOS TARGETARCH VERSION=dev
WORKDIR /src
COPY server/go.* ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY server/ .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /calendar-server ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /calendar-server /calendar-server
EXPOSE 8080
ENTRYPOINT ["/calendar-server"]

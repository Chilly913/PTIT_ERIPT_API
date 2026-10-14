# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS builder
WORKDIR /src
ENV CGO_ENABLED=0 GOOS=linux
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server && \
    go build -trimpath -ldflags="-s -w" -o /out/cli ./cmd/cli && \
    go build -trimpath -ldflags="-s -w" -o /out/worker ./cmd/worker

# distroless/static: CA certs + tzdata, no shell. Migrations are embedded in /cli.
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /
COPY --from=builder /out/server /out/cli /out/worker /
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/server"]

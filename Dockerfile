ARG GO_VERSION=1.25

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build
WORKDIR /src

RUN apk add --no-cache git

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ENV CGO_ENABLED=0
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -ldflags "-s -w -X main.version=${VERSION}" -o /out/punchd ./cmd/punchd && \
    GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -ldflags "-s -w -X main.version=${VERSION}" -o /out/punchctl ./cmd/punchctl

FROM debian:12-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends iproute2 iptables ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/punchd /usr/bin/punchd
COPY --from=build /out/punchctl /usr/bin/punchctl

ENV PUNCH_DATA_DIR=/var/lib/punch
VOLUME ["/var/lib/punch"]

ENTRYPOINT ["/usr/bin/punchd"]

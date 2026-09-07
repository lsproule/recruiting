# golang:1.27-alpine
FROM golang@sha256:4c9fe60190a2a3350ddc51de80d0224b8a6698d12bdfc999fee45ea9d6c46dbc AS harness
WORKDIR /src
COPY go.mod go.sum ./
COPY runner/harness ./runner/harness
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /harness ./runner/harness

# debian:bookworm-slim
FROM debian@sha256:88200866dfff7ea7f5cbcb6ec7c8a701889efe6fe859fe64d6990e4b07ea4171
COPY --from=harness /harness /usr/local/bin/harness
RUN apt-get update && apt-get install -y --no-install-recommends lua5.4 \
    && ln -s /usr/bin/lua5.4 /usr/local/bin/lua \
    && rm -rf /var/lib/apt/lists/*
USER 65534:65534
ENTRYPOINT ["/usr/local/bin/harness"]

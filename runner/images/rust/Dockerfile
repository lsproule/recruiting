# golang:1.27-alpine
FROM golang@sha256:4c9fe60190a2a3350ddc51de80d0224b8a6698d12bdfc999fee45ea9d6c46dbc AS harness
WORKDIR /src
COPY go.mod go.sum ./
COPY runner/harness ./runner/harness
COPY runner/wire ./runner/wire
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /harness ./runner/harness

# rust:1-slim-bookworm
FROM rust@sha256:1469a27c125cb5a3aebfa4f4e4665d935b02fb72cc093b2c974b3d740e43f157
COPY --from=harness /harness /usr/local/bin/harness
# Nothing to pre-warm: rustc keeps no on-disk cache and the image ships a compiled std.
USER 65534:65534
ENTRYPOINT ["/usr/local/bin/harness"]

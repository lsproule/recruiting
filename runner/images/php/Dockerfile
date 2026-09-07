# golang:1.27-alpine
FROM golang@sha256:4c9fe60190a2a3350ddc51de80d0224b8a6698d12bdfc999fee45ea9d6c46dbc AS harness
WORKDIR /src
COPY go.mod go.sum ./
COPY runner/harness ./runner/harness
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /harness ./runner/harness

# php:8.3-cli-bookworm
FROM php@sha256:177529735599a8244b2c903522f029839dce1c2ac4be122fdc00ada4b45a20e4
COPY --from=harness /harness /usr/local/bin/harness
USER 65534:65534
ENTRYPOINT ["/usr/local/bin/harness"]

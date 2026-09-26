# golang:1.27-alpine
FROM golang@sha256:4c9fe60190a2a3350ddc51de80d0224b8a6698d12bdfc999fee45ea9d6c46dbc AS harness
WORKDIR /src
COPY go.mod go.sum ./
COPY runner/harness ./runner/harness
COPY runner/wire ./runner/wire
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /harness ./runner/harness

# gcc:14-bookworm
FROM gcc@sha256:5e927c284bf55a7dc796262e311a0703344f62f41f5621eb56843111b1d37e15
COPY --from=harness /harness /usr/local/bin/harness
# Nothing to pre-warm: g++ keeps no on-disk cache, and the headers a single
# translation unit needs are already in the image.
USER 65534:65534
ENTRYPOINT ["/usr/local/bin/harness"]

# golang:1.27-alpine
FROM golang@sha256:4c9fe60190a2a3350ddc51de80d0224b8a6698d12bdfc999fee45ea9d6c46dbc AS harness
WORKDIR /src
COPY go.mod go.sum ./
COPY runner/harness ./runner/harness
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /harness ./runner/harness

# ruby:3.3-slim-bookworm
FROM ruby@sha256:df209d82bb55aeb26b9b08922ee22c15f19b3458455690132dc07a5c52dc1c99
COPY --from=harness /harness /usr/local/bin/harness
USER 65534:65534
ENTRYPOINT ["/usr/local/bin/harness"]

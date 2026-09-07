# golang:1.27-alpine
FROM golang@sha256:4c9fe60190a2a3350ddc51de80d0224b8a6698d12bdfc999fee45ea9d6c46dbc AS harness
WORKDIR /src
COPY go.mod go.sum ./
COPY runner/harness ./runner/harness
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /harness ./runner/harness

# haskell:9.6-slim-bullseye
FROM haskell@sha256:3abe1175a5466fe760ed90937b32b42be340281b6707dace6d5addea2148d05e
COPY --from=harness /harness /usr/local/bin/harness
# Nothing to pre-warm: ghc keeps no on-disk cache and the image ships a compiled base.
USER 65534:65534
ENTRYPOINT ["/usr/local/bin/harness"]

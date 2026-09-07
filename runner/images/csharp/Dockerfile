# golang:1.27-alpine
FROM golang@sha256:4c9fe60190a2a3350ddc51de80d0224b8a6698d12bdfc999fee45ea9d6c46dbc AS harness
WORKDIR /src
COPY go.mod go.sum ./
COPY runner/harness ./runner/harness
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /harness ./runner/harness

# mcr.microsoft.com/dotnet/sdk:9.0
FROM mcr.microsoft.com/dotnet/sdk@sha256:f190d2dd9eef2899c91ac323caa0bd2b39334a5400ba93013e5199da39dad940
COPY --from=harness /harness /usr/local/bin/harness
# NUGET_PACKAGES and the MSBuild caches must be pre-populated: the sandbox has
# no network, so a restore that needs anything at run time fails outright.
ENV DOTNET_CLI_TELEMETRY_OPTOUT=1 DOTNET_NOLOGO=1 DOTNET_SKIP_FIRST_TIME_EXPERIENCE=1 \
    DOTNET_CLI_HOME=/tmp NUGET_PACKAGES=/opt/nuget MSBUILDDISABLENODEREUSE=1 \
    DOTNET_EnableWriteXorExecute=0
# Same project shape the harness writes, built once so the SDK's targets,
# assets, and package cache are already on disk.
RUN mkdir -p /opt/nuget /opt/template && cd /opt/template \
    && printf '%s\n' '<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><OutputType>Exe</OutputType><TargetFramework>net9.0</TargetFramework><AssemblyName>candidate</AssemblyName><Nullable>disable</Nullable><ImplicitUsings>enable</ImplicitUsings></PropertyGroup></Project>' > candidate.csproj \
    && printf '%s\n' 'System.Console.WriteLine("warm");' > Program.cs \
    && dotnet build --nologo -c Release -o /opt/template/out \
    && chmod -R a+rX /opt/nuget /opt/template
USER 65534:65534
ENTRYPOINT ["/usr/local/bin/harness"]

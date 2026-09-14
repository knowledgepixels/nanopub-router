FROM golang:1.22-alpine AS build
# Set by the release pipeline so the binary can report its version; see release.config.js.
ARG VERSION=dev
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/nanopub-router .

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/nanopub-router /nanopub-router
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/nanopub-router"]

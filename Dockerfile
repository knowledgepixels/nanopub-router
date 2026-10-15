FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/nanopub-router .

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/nanopub-router /nanopub-router
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/nanopub-router"]

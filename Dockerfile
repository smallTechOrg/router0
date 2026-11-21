# router0 gateway image (Cloud Run). Build context: repo root.
#   docker build -t router0-gateway .
FROM golang:1.22-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0 GO111MODULE=on
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN go build -trimpath -ldflags="-s -w" -o /out/router ./cmd/router

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/router /app/router
COPY migrations /app/migrations
# main.go reads MIGRATIONS_PATH (its source-relative default is wrong in an image).
ENV MIGRATIONS_PATH=/app/migrations
# Cloud Run injects PORT; the gateway binds :$PORT.
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/app/router"]

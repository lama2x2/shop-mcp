# Build: docker build -t shop-mcp .
# Run:   docker run -i --rm shop-mcp
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
ARG VERSION=docker
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/shop-mcp ./cmd/shop-mcp

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/shop-mcp /usr/local/bin/shop-mcp
COPY shop.db /data/shop.db
ENV SHOP_DB_PATH=/data/shop.db
ENTRYPOINT ["/usr/local/bin/shop-mcp"]

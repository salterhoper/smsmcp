# Server image. The Android gateway connects to it; agents talk MCP to it.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server/ ./
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/smsmcp ./cmd/smsmcp

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/smsmcp /usr/local/bin/smsmcp
ENV SMSMCP_DATA=/data SMSMCP_ADDR=:8080
VOLUME ["/data"]
EXPOSE 8080
ENTRYPOINT ["smsmcp"]
CMD ["serve"]

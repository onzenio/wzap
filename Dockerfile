# The manager console (manager/) is a static Nuxt build embedded into the Go
# binary via go:embed (manager/manager.go). Node/pnpm live only in this
# stage; the Go stage below copies just the generated public dir.
FROM node:24-slim AS manager-build

RUN corepack enable

WORKDIR /src/manager

COPY manager/package.json manager/pnpm-lock.yaml manager/pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile

COPY manager/ ./
RUN pnpm build

FROM golang:1.26 AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
COPY --from=manager-build /src/manager/.output/public /src/manager/.output/public
RUN CGO_ENABLED=0 go build -o /out/wzap ./cmd/wzap
RUN mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/wzap /wzap
COPY --from=build --chown=65532:65532 /out/data /data

USER nonroot:nonroot

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=10s --start-period=10s --retries=3 \
    CMD ["/wzap", "healthcheck"]

ENTRYPOINT ["/wzap"]
CMD ["serve"]

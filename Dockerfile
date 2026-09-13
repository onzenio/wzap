FROM golang:1.26 AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
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

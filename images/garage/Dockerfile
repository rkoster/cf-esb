FROM golang:1.25-alpine AS entrypoint-builder
WORKDIR /src
COPY entrypoint/main.go ./main.go
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /garage-entrypoint ./main.go

FROM docker.io/dxflrs/garage:v2.3.0
COPY --from=entrypoint-builder /garage-entrypoint /garage-entrypoint
EXPOSE 3900
ENTRYPOINT ["/garage-entrypoint"]

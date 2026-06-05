FROM alpine:latest

RUN apk --no-cache add ca-certificates tzdata wget && \
    adduser -D -s /bin/sh olla && \
    mkdir -p /app && \
    chown -R olla:olla /app

WORKDIR /app

COPY . .
COPY olla /usr/local/bin/olla

RUN sh ./scripts/generate-container-config.sh
RUN chown -R olla:olla /app

USER olla

ENV OLLA_CONFIG_FILE=/app/config/docker.yaml

HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:40114/internal/ready || exit 1

EXPOSE 40114
ENTRYPOINT ["olla"]

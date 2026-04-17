FROM gcr.io/distroless/static-debian12:nonroot

COPY joghd /usr/bin/joghd
COPY configs/config.example.toml /etc/joghd/config.example.toml

USER nonroot:nonroot

ENTRYPOINT ["/usr/bin/joghd"]

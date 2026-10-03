FROM gcr.io/distroless/static-debian13:nonroot

COPY joghd /usr/bin/joghd
COPY configs/config.example.toml /etc/joghd/config.example.toml

USER nonroot:nonroot

ENTRYPOINT ["/usr/bin/joghd"]

# The working directory holds no config, so default to the conventional
# location operators mount over. Override by passing your own arguments.
CMD ["-config", "/etc/joghd/config.toml"]

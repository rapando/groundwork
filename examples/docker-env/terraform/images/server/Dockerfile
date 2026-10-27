# The server image: plain Debian plus what Ansible needs to manage it
# (Python, the apt bindings and ps), like a cloud provider's base image.
FROM debian:bookworm-slim
RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends python3 python3-apt procps \
 && rm -rf /var/lib/apt/lists/*

project      = "docker-env"
network_cidr = "172.30.1.0/24"

# Each host port is 10000 + the container port, so they don't clash with
# services already running on this machine.
servers = {
  rabbitmq = {
    ports = [
      { internal = 5672, external = 15672 },  # AMQP
      { internal = 15672, external = 25672 }, # management UI
    ]
  }
  redis = {
    ports = [
      { internal = 6379, external = 16379 },
    ]
  }
  nginx = {
    ports = [
      { internal = 80, external = 18080 },
    ]
  }
}

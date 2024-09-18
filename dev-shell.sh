down() {
    docker compose --env-file=test/compose/.env.test -f test/compose/docker-compose.yml down --remove-orphans
	# remove the docker runtime and compose app store volumes
    docker volume rm compose_docker-runtime compose_reset-apps
}

trap down EXIT

docker compose --env-file=test/compose/.env.test -f test/compose/docker-compose.yml run composectl $@

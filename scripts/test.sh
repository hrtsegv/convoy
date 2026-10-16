#!/usr/bin/env bash
set -euo pipefail

cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.."
go_command="${GO:-go}"
if ! command -v docker >/dev/null; then
    printf '%s\n' 'Docker is required for the full test suite; use make test-unit for unit tests.' >&2
    exit 1
fi
if ! docker info >/dev/null 2>&1; then
    printf '%s\n' 'Docker must be running and accessible for the full test suite.' >&2
    exit 1
fi

test_directory=$(mktemp -d "${TMPDIR:-/tmp}/convoy-test.XXXXXXXX")
cleanup() {
    status=$?
    trap - EXIT
    if [[ -s "$test_directory/postgres.cid" ]]; then
        container_id=$(cat "$test_directory/postgres.cid")
        if ! docker rm -f -v "$container_id" >/dev/null; then
            printf '%s\n' 'Could not remove the test database container.' >&2
            if [[ $status -eq 0 ]]; then
                status=1
            fi
        fi
    fi
    rm -f -- "$test_directory/postgres.cid"
    rmdir -- "$test_directory"
    exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

printf '%s\n' 'Starting a fresh PostgreSQL test database...'
docker run -d \
    --cidfile "$test_directory/postgres.cid" \
    --label com.convoy.test=true \
    --publish 127.0.0.1::5432 \
    --env POSTGRES_USER=convoy \
    --env POSTGRES_PASSWORD=convoy-local-test \
    --env POSTGRES_DB=convoy_test \
    postgres:17-alpine >/dev/null
container_id=$(cat "$test_directory/postgres.cid")

ready=false
for ((attempt = 0; attempt < 30; attempt++)); do
    # TCP excludes the temporary Unix-socket server used during image setup.
    if docker exec "$container_id" pg_isready -h 127.0.0.1 -U convoy -d convoy_test >/dev/null 2>&1; then
        ready=true
        break
    fi
    if [[ $(docker inspect --format '{{.State.Running}}' "$container_id") != true ]]; then
        break
    fi
    sleep 1
done
if [[ $ready != true ]]; then
    printf '%s\n' 'PostgreSQL did not become ready.' >&2
    docker logs "$container_id" >&2
    exit 1
fi

database_address=$(docker port "$container_id" 5432/tcp)
export CONVOY_TEST_DATABASE_URL="postgres://convoy:convoy-local-test@${database_address}/convoy_test?sslmode=disable&connect_timeout=5"
printf '%s\n' 'Applying migrations to the fresh database...'
CONVOY_DATABASE_URL="$CONVOY_TEST_DATABASE_URL" "$go_command" run ./cmd/migrate up

printf '%s\n' 'Running unit and integration tests...'
"$go_command" test -count=1 -tags=integration -timeout=60s "$@" ./...

#!/usr/bin/env bash

set -Eeuo pipefail

LABEL_KEY="wp-panel.test"
LABEL_VALUE="wp-inventory-runner-g3b"
PREFIX="wp-panel-runner-g3b"
WP_CONTAINER="${PREFIX}-wp"
DB_CONTAINER="${PREFIX}-db"
NETWORK_NAME="${PREFIX}-network"
VOLUME_NAME="${PREFIX}-wordpress"
SENTINEL_CONTAINER="${PREFIX}-foreign-label-sentinel"
SENTINEL_CREATED_ID=""
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
TEST_BINARY="$(mktemp /tmp/wp-panel-runner-g3b-executor-test.XXXXXX)"
RESULTS_DIR="$(mktemp -d /tmp/wp-panel-runner-g3b-results.XXXXXX)"

WORDPRESS_IMAGE="${WORDPRESS_IMAGE:-wordpress:7.0-php8.3-apache}"
WORDPRESS_CLI_IMAGE="${WORDPRESS_CLI_IMAGE:-wordpress:cli-php8.3}"
MARIADB_IMAGE="${MARIADB_IMAGE:-mariadb:11.8}"
DB_ROOT_PASSWORD="g3b-root-password"
DB_NAME="wordpress"
DB_USER="wordpress"
DB_PASSWORD="g3b-wordpress-password"
SITE_USER="wp_runner_test"
SITE_UID="1200"
SITE_GID="1200"
DOMAIN="wp-panel-runner-g3b.invalid"

show_help() {
    cat <<'EOF'
Run the disposable local Docker validation for the production WP inventory Runner.

No ports or host site/database paths are mounted. Resources use the prefix
wp-panel-runner-g3b-* and label wp-panel.test=wp-inventory-runner-g3b.

If this script is killed with SIGKILL, inspect labels before manual cleanup:
  docker container inspect wp-panel-runner-g3b-wp --format '{{ index .Config.Labels "wp-panel.test" }}'
  docker container inspect wp-panel-runner-g3b-db --format '{{ index .Config.Labels "wp-panel.test" }}'
  docker volume inspect wp-panel-runner-g3b-wordpress --format '{{ index .Labels "wp-panel.test" }}'
  docker network inspect wp-panel-runner-g3b-network --format '{{ index .Labels "wp-panel.test" }}'

Only when each value is exactly wp-inventory-runner-g3b, remove those exact names:
  docker rm -f wp-panel-runner-g3b-wp wp-panel-runner-g3b-db
  docker volume rm wp-panel-runner-g3b-wordpress
  docker network rm wp-panel-runner-g3b-network

Never use docker system prune or wildcard deletion for this validation.
EOF
}

if [[ "${1:-}" == "--help" ]]; then
    show_help
    exit 0
fi

resource_label() {
    local kind="$1" name="$2"
    case "$kind" in
        container) docker container inspect --format "{{ index .Config.Labels \"${LABEL_KEY}\" }}" "$name" 2>/dev/null || true ;;
        network) docker network inspect --format "{{ index .Labels \"${LABEL_KEY}\" }}" "$name" 2>/dev/null || true ;;
        volume) docker volume inspect --format "{{ index .Labels \"${LABEL_KEY}\" }}" "$name" 2>/dev/null || true ;;
    esac
}

remove_exact_owned_resource() {
    local kind="$1" name="$2" label
    label="$(resource_label "$kind" "$name")"
    if [[ -z "$label" ]]; then
        return 0
    fi
    if [[ "$label" != "$LABEL_VALUE" ]]; then
        printf 'Refusing to remove %s %s with label %q\n' "$kind" "$name" "$label" >&2
        return 1
    fi
    case "$kind" in
        container) docker rm -f "$name" >/dev/null ;;
        network) docker network rm "$name" >/dev/null ;;
        volume) docker volume rm "$name" >/dev/null ;;
    esac
}

cleanup() {
    remove_exact_owned_resource container "$WP_CONTAINER" >/dev/null 2>&1 || true
    remove_exact_owned_resource container "$DB_CONTAINER" >/dev/null 2>&1 || true
    remove_exact_owned_resource volume "$VOLUME_NAME" >/dev/null 2>&1 || true
    remove_exact_owned_resource network "$NETWORK_NAME" >/dev/null 2>&1 || true
    if [[ -n "$SENTINEL_CREATED_ID" ]]; then
        current_id="$(docker container inspect --format '{{.Id}}' "$SENTINEL_CONTAINER" 2>/dev/null || true)"
        if [[ "$current_id" == "$SENTINEL_CREATED_ID" ]]; then
            docker rm -f "$SENTINEL_CONTAINER" >/dev/null 2>&1 || true
        fi
    fi
    rm -f "$TEST_BINARY"
}
trap cleanup EXIT INT TERM

for pair in "container:$WP_CONTAINER" "container:$DB_CONTAINER" "volume:$VOLUME_NAME" "network:$NETWORK_NAME"; do
    kind="${pair%%:*}"
    name="${pair#*:}"
    remove_exact_owned_resource "$kind" "$name"
done

if docker container inspect "$SENTINEL_CONTAINER" >/dev/null 2>&1; then
    printf 'A pre-existing foreign-label sentinel is present; verifying it is preserved\n'
else
    SENTINEL_CREATED_ID="$(docker create --name "$SENTINEL_CONTAINER" --label "${LABEL_KEY}=foreign" "$WORDPRESS_IMAGE")"
fi
if remove_exact_owned_resource container "$SENTINEL_CONTAINER" >/dev/null 2>&1; then
    printf 'Label guard unexpectedly accepted a foreign-labeled resource\n' >&2
    exit 1
fi
docker container inspect "$SENTINEL_CONTAINER" >/dev/null
if [[ -n "$SENTINEL_CREATED_ID" ]]; then
    docker rm "$SENTINEL_CONTAINER" >/dev/null
    SENTINEL_CREATED_ID=""
fi

docker network create --label "${LABEL_KEY}=${LABEL_VALUE}" "$NETWORK_NAME" >/dev/null
docker volume create --label "${LABEL_KEY}=${LABEL_VALUE}" "$VOLUME_NAME" >/dev/null
docker run -d --name "$DB_CONTAINER" --label "${LABEL_KEY}=${LABEL_VALUE}" --network "$NETWORK_NAME" \
    -e "MARIADB_ROOT_PASSWORD=${DB_ROOT_PASSWORD}" -e "MARIADB_DATABASE=${DB_NAME}" \
    -e "MARIADB_USER=${DB_USER}" -e "MARIADB_PASSWORD=${DB_PASSWORD}" "$MARIADB_IMAGE" >/dev/null

for attempt in $(seq 1 60); do
    if docker exec "$DB_CONTAINER" mariadb-admin ping -uroot "-p${DB_ROOT_PASSWORD}" --silent >/dev/null 2>&1; then break; fi
    if [[ "$attempt" == "60" ]]; then printf 'MariaDB did not become ready\n' >&2; exit 1; fi
    sleep 2
done

docker run -d --name "$WP_CONTAINER" --label "${LABEL_KEY}=${LABEL_VALUE}" --network "$NETWORK_NAME" \
    -e "WORDPRESS_DB_HOST=${DB_CONTAINER}:3306" -e "WORDPRESS_DB_NAME=${DB_NAME}" \
    -e "WORDPRESS_DB_USER=${DB_USER}" -e "WORDPRESS_DB_PASSWORD=${DB_PASSWORD}" \
    -v "${VOLUME_NAME}:/var/www/html" "$WORDPRESS_IMAGE" >/dev/null

for attempt in $(seq 1 60); do
    if docker exec "$WP_CONTAINER" test -f /var/www/html/wp-config.php >/dev/null 2>&1; then break; fi
    if [[ "$attempt" == "60" ]]; then printf 'WordPress files did not become ready\n' >&2; exit 1; fi
    sleep 2
done

docker exec "$WP_CONTAINER" test -x /usr/sbin/runuser
docker exec "$WP_CONTAINER" test -x /usr/local/bin/php
docker exec "$WP_CONTAINER" cp /usr/local/bin/php /usr/bin/php8.3
docker exec "$WP_CONTAINER" chown root:root /usr/bin/php8.3
docker exec "$WP_CONTAINER" chmod 0755 /usr/bin/php8.3
docker exec "$WP_CONTAINER" /usr/sbin/groupadd -g "$SITE_GID" "$SITE_USER"
docker exec "$WP_CONTAINER" /usr/sbin/useradd -u "$SITE_UID" -g "$SITE_GID" -d /var/www/html -s /usr/sbin/nologin "$SITE_USER"

run_wp_cli() {
    docker run --rm --network "$NETWORK_NAME" --volumes-from "$WP_CONTAINER" --user "${SITE_UID}:${SITE_GID}" \
        -e "WORDPRESS_DB_HOST=${DB_CONTAINER}:3306" -e "WORDPRESS_DB_NAME=${DB_NAME}" \
        -e "WORDPRESS_DB_USER=${DB_USER}" -e "WORDPRESS_DB_PASSWORD=${DB_PASSWORD}" \
        "$WORDPRESS_CLI_IMAGE" wp "$@"
}

docker exec "$WP_CONTAINER" chown -R "${SITE_UID}:${SITE_GID}" /var/www/html
run_wp_cli core install --path=/var/www/html --url="http://${DOMAIN}" --title='WP Panel Runner G3-B' \
    --admin_user=g3b-admin --admin_password=g3b-admin-password --admin_email=g3b@example.invalid --skip-email
# The official image generates getenv-backed database constants. Production sites use
# literal wp-config.php values, and the Runner intentionally does not inherit container
# database environment variables, so make this disposable site match production.
run_wp_cli config set DB_HOST "${DB_CONTAINER}:3306" --path=/var/www/html >/dev/null
run_wp_cli config set DB_NAME "$DB_NAME" --path=/var/www/html >/dev/null
run_wp_cli config set DB_USER "$DB_USER" --path=/var/www/html >/dev/null
run_wp_cli config set DB_PASSWORD "$DB_PASSWORD" --path=/var/www/html >/dev/null

docker exec "$WP_CONTAINER" mkdir -p /var/www/html/wp-content/plugins/wp-panel-runner-adversarial
docker cp "${PROJECT_DIR}/executor/testdata/wp_inventory_runner/adversarial-plugin.php" \
    "$WP_CONTAINER:/var/www/html/wp-content/plugins/wp-panel-runner-adversarial/wp-panel-runner-adversarial.php"
docker exec "$WP_CONTAINER" chown -R "${SITE_UID}:${SITE_GID}" /var/www/html/wp-content/plugins/wp-panel-runner-adversarial
run_wp_cli plugin activate wp-panel-runner-adversarial --path=/var/www/html

docker exec "$WP_CONTAINER" cp /etc/hostname /opt/wp-panel-runner-forbidden.txt
docker exec "$WP_CONTAINER" chmod 0644 /opt/wp-panel-runner-forbidden.txt
docker exec --user "${SITE_UID}:${SITE_GID}" "$WP_CONTAINER" cat /opt/wp-panel-runner-forbidden.txt >"${RESULTS_DIR}/forbidden-readable.txt"

(
    cd "$PROJECT_DIR"
    CGO_ENABLED=0 go test -c ./executor -o "$TEST_BINARY"
)
docker cp "$TEST_BINARY" "$WP_CONTAINER:/tmp/wp-panel-runner-g3b-executor.test"
docker exec "$WP_CONTAINER" chown root:root /tmp/wp-panel-runner-g3b-executor.test
docker exec "$WP_CONTAINER" chmod 0755 /tmp/wp-panel-runner-g3b-executor.test

set_mode() {
    run_wp_cli config set WP_PANEL_TEST_MODE "$1" --path=/var/www/html >/dev/null
}

run_e2e() {
    local name="$1" expected="$2" repeat="${3:-1}" multisite="${4:-0}" plugin_transient="${5:-}"
    printf '\nCASE %s\n' "$name"
    docker exec \
        -e WP_PANEL_RUNNER_E2E=1 \
        -e WP_PANEL_RUNNER_SITE_ROOT=/var/www/html \
        -e WP_PANEL_RUNNER_WWW_ROOT=/var/www \
        -e "WP_PANEL_RUNNER_DOMAIN=${DOMAIN}" \
        -e "WP_PANEL_RUNNER_USER=${SITE_USER}" \
        -e "WP_PANEL_RUNNER_EXPECT_CODE=${expected}" \
        -e "WP_PANEL_RUNNER_REPEAT=${repeat}" \
        -e "WP_PANEL_RUNNER_REQUIRE_MULTISITE=${multisite}" \
        -e "WP_PANEL_RUNNER_EXPECT_PLUGIN_TRANSIENT=${plugin_transient}" \
        "$WP_CONTAINER" /tmp/wp-panel-runner-g3b-executor.test -test.run '^TestWPInventoryRunnerE2E$' -test.v \
        | tee "${RESULTS_DIR}/${name}.txt"
}

run_failure_and_recovery() {
    local mode="$1" expected="$2"
    set_mode "$mode"
    run_e2e "$mode" "$expected"
    set_mode normal
    run_e2e "recovery-after-${mode}" ""
}

set_mode normal
run_e2e normal ""
run_wp_cli eval 'delete_site_transient("update_plugins");' --path=/var/www/html >/dev/null
run_e2e update-transient-absent "" 1 0 absent
run_wp_cli eval '$value = (object) array("last_checked" => 1784500000, "response" => array()); set_site_transient("update_plugins", $value);' --path=/var/www/html >/dev/null
run_e2e update-transient-present-empty "" 1 0 present
set_mode noise
run_e2e noise ""
set_mode security
run_e2e security ""
run_failure_and_recovery fatal wordpress_bootstrap_failed
run_failure_and_recovery exit wordpress_terminated
run_failure_and_recovery timeout runner_timeout
run_failure_and_recovery timeout_child runner_timeout
if docker exec "$WP_CONTAINER" pgrep -f '/var/wp-panel/runners/wp-inventory/.*/inventory.php' >/dev/null 2>&1; then
    printf 'A timed-out Runner process remains in the container\n' >&2
    exit 1
fi
run_failure_and_recovery memory memory_limit_exhausted
run_failure_and_recovery stdout_limit stdout_limit_exceeded
run_failure_and_recovery stderr_limit stderr_limit_exceeded
run_failure_and_recovery protocol_spoof protocol_invalid

set_mode normal
for samples in 10 50 100; do
    run_e2e "scale-${samples}" "" "$samples"
done

run_wp_cli core multisite-convert --path=/var/www/html --title='WP Panel Runner G3-B Network'
run_wp_cli config set WP_ALLOW_MULTISITE true --raw --path=/var/www/html >/dev/null
run_wp_cli config set MULTISITE true --raw --path=/var/www/html >/dev/null
run_wp_cli config set SUBDOMAIN_INSTALL false --raw --path=/var/www/html >/dev/null
run_wp_cli config set DOMAIN_CURRENT_SITE "$DOMAIN" --path=/var/www/html >/dev/null
run_wp_cli config set PATH_CURRENT_SITE / --path=/var/www/html >/dev/null
run_wp_cli config set SITE_ID_CURRENT_SITE 1 --raw --path=/var/www/html >/dev/null
run_wp_cli config set BLOG_ID_CURRENT_SITE 1 --raw --path=/var/www/html >/dev/null
run_wp_cli config set base / --type=variable --path=/var/www/html >/dev/null
set_mode normal
run_e2e multisite "" 1 1

docker exec "$WP_CONTAINER" /usr/bin/php8.3 -v | sed -n '1p'
run_wp_cli core version --path=/var/www/html
printf 'Validation evidence: %s\n' "$RESULTS_DIR"

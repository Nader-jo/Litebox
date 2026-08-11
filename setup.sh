#!/bin/sh
set -eu

# Git for Windows otherwise rewrites container paths such as /data before they
# reach Docker. The variable is ignored by regular Linux and macOS shells.
MSYS_NO_PATHCONV=1
export MSYS_NO_PATHCONV

repository="Nader-jo/Litebox"
image_repository="ghcr.io/nader-jo/litebox"
release_base_url=${LITEBOX_RELEASE_BASE_URL:-"https://github.com/$repository/releases"}
mode=install
version=${LITEBOX_VERSION:-}
install_dir=${LITEBOX_INSTALL_DIR:-/opt/litebox}
non_interactive=${LITEBOX_NON_INTERACTIVE:-0}
reconfigure=0
no_start=0
temporary_dir=
secret_echo_disabled=0

usage() {
	cat <<'EOF'
Litebox setup

Usage:
  sh setup.sh [options]
  sh setup.sh --demo [--version VERSION]

Options:
  --demo                 Start a local, no-email demo at http://localhost:8080.
  --version VERSION      Install a specific release (defaults to latest).
  --install-dir PATH     Install into PATH (defaults to /opt/litebox).
  --non-interactive      Read required configuration from environment variables.
  --reconfigure          Replace managed values in an existing .env file.
  --no-start             Download, configure, and validate without starting services.
  -h, --help             Show this help.

Non-interactive configuration:
  LITEBOX_HOST, MAILBOX_PRIMARY_ADDRESS, RESEND_API_KEY, and
  RESEND_WEBHOOK_SECRET are required for a new production installation.
  APP_BASE_URL, MAILBOX_DISPLAY_NAME, MAILBOX_ALLOWED_RECIPIENTS,
  RESEND_DOMAIN_ID, LITEBOX_PORT, and LITEBOX_USE_CADDY are optional.
EOF
}

log() {
	printf '%s\n' "==> $*"
}

die() {
	printf '%s\n' "error: $*" >&2
	exit 1
}

cleanup() {
	if [ "$secret_echo_disabled" -eq 1 ] && [ -w /dev/tty ]; then
		stty echo </dev/tty 2>/dev/null || true
	fi
	if [ -n "$temporary_dir" ] && [ -d "$temporary_dir" ]; then
		rm -rf "$temporary_dir"
	fi
}
trap cleanup 0
trap 'exit 130' INT
trap 'exit 143' HUP TERM

require_command() {
	command -v "$1" >/dev/null 2>&1 || die "$1 is required"
}

validate_version() {
	version=${version#v}
	printf '%s\n' "$version" | grep -Eq '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?$' ||
		die "invalid semantic version: $version"
}

resolve_version() {
	if [ -n "$version" ]; then
		validate_version
		return
	fi
	log "Resolving the latest stable release"
	latest_url=$(curl --fail --silent --show-error --location --head \
		--output /dev/null --write-out '%{url_effective}' "$release_base_url/latest") ||
		die "could not resolve the latest Litebox release"
	version=${latest_url##*/}
	validate_version
}

check_platform() {
	case "$(uname -m)" in
	x86_64 | amd64 | aarch64 | arm64) ;;
	*) die "unsupported host architecture $(uname -m); Litebox supports 64-bit AMD/Intel and ARM" ;;
	esac
}

check_docker() {
	require_command docker
	docker compose version >/dev/null 2>&1 || die "Docker Compose v2 is required (docker compose)"
	docker info >/dev/null 2>&1 || die "cannot reach the Docker daemon; start Docker or check your permissions"
}

prompt() {
	label=$1
	default_value=$2
	[ -r /dev/tty ] || die "interactive input needs a terminal; use --non-interactive"
	if [ -n "$default_value" ]; then
		printf '%s [%s]: ' "$label" "$default_value" >/dev/tty
	else
		printf '%s: ' "$label" >/dev/tty
	fi
	IFS= read -r reply </dev/tty || die "input ended unexpectedly"
	REPLY=${reply:-$default_value}
}

prompt_secret() {
	label=$1
	[ -r /dev/tty ] && [ -w /dev/tty ] || die "secret input needs a terminal; use --non-interactive"
	require_command stty
	printf '%s: ' "$label" >/dev/tty
	stty -echo </dev/tty
	secret_echo_disabled=1
	IFS= read -r REPLY </dev/tty || {
		stty echo </dev/tty
		secret_echo_disabled=0
		die "input ended unexpectedly"
	}
	stty echo </dev/tty
	secret_echo_disabled=0
	printf '\n' >/dev/tty
}

confirm() {
	label=$1
	default_answer=$2
	if [ "$default_answer" = 1 ]; then
		prompt "$label (Y/n)" "Y"
	else
		prompt "$label (y/N)" "N"
	fi
	case "$REPLY" in
	y | Y | yes | YES | Yes) REPLY=1 ;;
	n | N | no | NO | No) REPLY=0 ;;
	*) die "please answer yes or no" ;;
	esac
}

validate_single_line() {
	value=$1
	label=$2
	case "$value" in
	*'
'*) die "$label must be a single line" ;;
	esac
}

validate_host() {
	case "$1" in
	"" | .* | *. | *[!A-Za-z0-9.-]*) die "LITEBOX_HOST must be a DNS hostname without a scheme or path" ;;
	esac
}

validate_email() {
	printf '%s\n' "$1" | grep -Eq '^[^[:space:]@]+@[^[:space:]@]+\.[^[:space:]@]+$' ||
		die "invalid mailbox email address: $1"
}

validate_port() {
	case "$1" in
	"" | *[!0-9]*) die "LITEBOX_PORT must be an integer from 1 to 65535" ;;
	esac
	[ "$1" -ge 1 ] && [ "$1" -le 65535 ] ||
		die "LITEBOX_PORT must be an integer from 1 to 65535"
}

set_env() {
	key=$1
	value=$2
	validate_single_line "$value" "$key"
	escaped_value=$(printf '%s' "$value" | sed 's/[\\&|]/\\&/g')
	tmp_env=$(mktemp "${env_file}.XXXXXX")
	if grep -q "^${key}=" "$env_file"; then
		sed "s|^${key}=.*$|${key}=${escaped_value}|" "$env_file" >"$tmp_env"
	else
		{
			cat "$env_file"
			printf '%s=%s\n' "$key" "$value"
		} >"$tmp_env"
	fi
	chmod 0600 "$tmp_env"
	mv "$tmp_env" "$env_file"
}

get_env() {
	key=$1
	sed -n "s/^${key}=//p" "$env_file" | tail -n 1
}

sha256_file() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{print $1}'
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | awk '{print $1}'
	else
		die "sha256sum or shasum is required to verify the release bundle"
	fi
}

download() {
	url=$1
	destination=$2
	curl --fail --silent --show-error --location --retry 3 --connect-timeout 15 \
		--output "$destination" "$url"
}

wait_for_container() {
	container=$1
	attempt=0
	while [ "$attempt" -lt 45 ]; do
		status=$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$container" 2>/dev/null || true)
		case "$status" in
		healthy) return 0 ;;
		unhealthy | exited | dead)
			docker logs --tail 100 "$container" >&2 || true
			return 1
			;;
		esac
		attempt=$((attempt + 1))
		sleep 1
	done
	return 1
}

run_demo() {
	check_platform
	require_command curl
	check_docker
	resolve_version
	port=${LITEBOX_PORT:-8080}
	validate_port "$port"
	container=litebox-demo
	volume=litebox-demo-data
	if docker container inspect "$container" >/dev/null 2>&1; then
		owner=$(docker inspect --format '{{index .Config.Labels "io.litebox.setup-mode"}}' "$container")
		[ "$owner" = demo ] ||
			die "a container named $container already exists and is not managed by Litebox setup"
		existing_port=$(docker inspect --format '{{index .Config.Labels "io.litebox.demo-port"}}' "$container")
		[ "$existing_port" = "$port" ] ||
			die "the existing demo uses port $existing_port; rerun with LITEBOX_PORT=$existing_port"
		if [ "$(docker inspect --format '{{.State.Running}}' "$container")" != true ]; then
			log "Starting the existing Litebox demo"
			docker start "$container" >/dev/null
		else
			log "The Litebox demo is already running"
		fi
	else
		log "Starting Litebox $version in local demo mode"
		docker pull "$image_repository:$version" >/dev/null
		docker volume create "$volume" >/dev/null
		docker run --detach \
			--name "$container" \
			--label io.litebox.setup-mode=demo \
			--label "io.litebox.demo-port=$port" \
			--init \
			--read-only \
			--tmpfs /tmp:size=64m,mode=1777 \
			--cap-drop ALL \
			--security-opt no-new-privileges:true \
			--publish "127.0.0.1:${port}:8080" \
			--volume "$volume:/data" \
			--env APP_ENV=development \
			--env "APP_BASE_URL=http://localhost:${port}" \
			--env APP_LISTEN_ADDR=:8080 \
			--env APP_DATA_DIR=/data \
			--env APP_DB_PATH=/data/mailbox.db \
			--env STORAGE_ROOT=/data/objects \
			--env STORAGE_TMP_ROOT=/data/tmp \
			--env MAILBOX_PRIMARY_ADDRESS=demo@example.test \
			--env "MAILBOX_DISPLAY_NAME=Litebox Demo" \
			--env MAILBOX_ALLOWED_RECIPIENTS=demo@example.test \
			"$image_repository:$version" >/dev/null
	fi
	if ! wait_for_container "$container"; then
		die "the demo did not become healthy"
	fi
	cat <<EOF

Litebox is ready: http://localhost:${port}/setup

This private demo stores data in the Docker volume $volume and does not send or
receive real email.

Stop it:         docker stop $container
Start it again:  docker start $container
Delete all data: docker rm -f $container && docker volume rm $volume
EOF
}

configure_installation() {
	if [ "$non_interactive" -eq 1 ]; then
		host=${LITEBOX_HOST:-}
		primary=${MAILBOX_PRIMARY_ADDRESS:-}
		display_name=${MAILBOX_DISPLAY_NAME:-Litebox}
		allowed=${MAILBOX_ALLOWED_RECIPIENTS:-$primary}
		api_key=${RESEND_API_KEY:-}
		webhook_secret=${RESEND_WEBHOOK_SECRET:-}
		domain_id=${RESEND_DOMAIN_ID:-}
		use_caddy=${LITEBOX_USE_CADDY:-1}
		port=${LITEBOX_PORT:-8080}
		[ -n "$host" ] || die "LITEBOX_HOST is required with --non-interactive"
		[ -n "$primary" ] || die "MAILBOX_PRIMARY_ADDRESS is required with --non-interactive"
		[ -n "$api_key" ] || die "RESEND_API_KEY is required with --non-interactive"
		[ -n "$webhook_secret" ] || die "RESEND_WEBHOOK_SECRET is required with --non-interactive"
	else
		prompt "Public hostname" "mail.example.com"
		host=$REPLY
		prompt "Primary mailbox address" "hello@example.com"
		primary=$REPLY
		prompt "Mailbox display name" "Litebox"
		display_name=$REPLY
		prompt "Allowed recipient addresses (comma-separated)" "$primary"
		allowed=$REPLY
		prompt_secret "Resend API key"
		api_key=$REPLY
		prompt_secret "Resend webhook signing secret"
		webhook_secret=$REPLY
		prompt "Resend domain ID (optional)" ""
		domain_id=$REPLY
		confirm "Use the bundled Caddy HTTPS proxy" 1
		use_caddy=$REPLY
		port=8080
	fi
	validate_host "$host"
	validate_email "$primary"
	[ -n "$api_key" ] || die "Resend API key cannot be empty"
	[ -n "$webhook_secret" ] || die "Resend webhook signing secret cannot be empty"
	case "$use_caddy" in
	0 | 1) ;;
	*) die "LITEBOX_USE_CADDY must be 0 or 1" ;;
	esac
	validate_port "$port"
	base_url=${APP_BASE_URL:-"https://$host"}
	case "$base_url" in
	https://*) ;;
	*) die "APP_BASE_URL must use HTTPS in production" ;;
	esac

	set_env APP_ENV production
	set_env APP_BASE_URL "$base_url"
	set_env MAILBOX_PRIMARY_ADDRESS "$primary"
	set_env MAILBOX_DISPLAY_NAME "$display_name"
	set_env MAILBOX_ALLOWED_RECIPIENTS "$allowed"
	set_env RESEND_API_KEY "$api_key"
	set_env RESEND_WEBHOOK_SECRET "$webhook_secret"
	set_env RESEND_DOMAIN_ID "$domain_id"
	set_env LITEBOX_HOST "$host"
	set_env LITEBOX_PORT "$port"
	set_env LITEBOX_USE_CADDY "$use_caddy"
}

run_install() {
	check_platform
	require_command curl
	require_command tar
	require_command grep
	require_command sed
	require_command awk
	check_docker
	resolve_version

	temporary_dir=$(mktemp -d)
	bundle_name="litebox_${version}_vps.tar.gz"
	bundle="$temporary_dir/$bundle_name"
	checksum="$bundle.sha256"
	download_url="$release_base_url/download/v${version}"
	log "Downloading Litebox $version"
	download "$download_url/$bundle_name" "$bundle"
	download "$download_url/$bundle_name.sha256" "$checksum"
	expected=$(awk 'NR == 1 {print tolower($1)}' "$checksum")
	actual=$(sha256_file "$bundle" | tr 'A-F' 'a-f')
	[ -n "$expected" ] && [ "$expected" = "$actual" ] || die "release bundle checksum verification failed"
	if tar -tzf "$bundle" | grep -Eq '(^/|(^|/)\.\.(/|$))'; then
		die "release bundle contains an unsafe path"
	fi
	mkdir "$temporary_dir/extracted"
	tar -xzf "$bundle" -C "$temporary_dir/extracted"
	for required in compose.yaml Caddyfile .env.example DEPLOYMENT.md LICENSE NOTICE; do
		[ -f "$temporary_dir/extracted/$required" ] || die "release bundle is missing $required"
	done

	if ! mkdir -p "$install_dir" 2>/dev/null; then
		die "cannot create $install_dir; rerun with sudo or choose --install-dir"
	fi
	for file in compose.yaml Caddyfile .env.example DEPLOYMENT.md UPGRADING.md BACKUP_AND_RESTORE.md USER_GUIDE.md LICENSE NOTICE setup.sh; do
		if [ -f "$temporary_dir/extracted/$file" ]; then
			cp "$temporary_dir/extracted/$file" "$install_dir/$file"
		fi
	done
	[ -f "$install_dir/setup.sh" ] && chmod 0755 "$install_dir/setup.sh"
	env_file="$install_dir/.env"
	new_install=0
	if [ ! -f "$env_file" ]; then
		cp "$install_dir/.env.example" "$env_file"
		new_install=1
	else
		log "Preserving the existing $env_file"
	fi
	chmod 0600 "$env_file"
	set_env LITEBOX_IMAGE "$image_repository:$version"

	if [ "$new_install" -eq 1 ] || [ "$reconfigure" -eq 1 ]; then
		configure_installation
	else
		use_caddy=$(get_env LITEBOX_USE_CADDY)
		# Installations created before this setting existed may already have a
		# reverse proxy on ports 80/443, so preserve the safer legacy behavior.
		use_caddy=${use_caddy:-0}
		case "$use_caddy" in
		0 | 1) ;;
		*) die "existing LITEBOX_USE_CADDY must be 0 or 1" ;;
		esac
	fi

	cd "$install_dir"
	docker compose config --quiet || die "generated Compose configuration is invalid"
	if [ "$no_start" -eq 1 ]; then
		cat <<EOF

Litebox $version is configured in $install_dir.
Start it with: cd $install_dir && docker compose up -d
EOF
		return
	fi

	log "Pulling and starting Litebox"
	if [ "$use_caddy" -eq 1 ]; then
		docker compose --profile proxy pull
		docker compose --profile proxy up -d
	else
		docker compose pull mailbox
		docker compose up -d mailbox
	fi
	container=$(docker compose ps -q mailbox)
	[ -n "$container" ] || die "Litebox container was not created"
	if ! wait_for_container "$container"; then
		docker compose logs --tail 100 mailbox >&2 || true
		die "Litebox did not become healthy"
	fi
	base_url=$(get_env APP_BASE_URL)
	host=$(get_env LITEBOX_HOST)
	upgrade_guide="$install_dir/UPGRADING.md"
	[ -f "$upgrade_guide" ] || upgrade_guide="https://github.com/$repository/blob/develop/docs/UPGRADING.md"
	cat <<EOF

Litebox $version is ready.

1. Create the first administrator: ${base_url}/setup
2. Add this Resend webhook:      https://${host}/webhooks/resend
3. Follow the DNS checklist:     https://github.com/$repository/blob/v${version}/docs/RESEND_SETUP.md

Installation directory: $install_dir
Status command:          cd $install_dir && docker compose ps
Upgrade guide:           $upgrade_guide
EOF
}

while [ "$#" -gt 0 ]; do
	case "$1" in
	--demo)
		mode=demo
		shift
		;;
	--version)
		[ "$#" -ge 2 ] || die "--version requires a value"
		version=$2
		shift 2
		;;
	--install-dir)
		[ "$#" -ge 2 ] || die "--install-dir requires a value"
		install_dir=$2
		shift 2
		;;
	--non-interactive)
		non_interactive=1
		shift
		;;
	--reconfigure)
		reconfigure=1
		shift
		;;
	--no-start)
		no_start=1
		shift
		;;
	-h | --help)
		usage
		exit 0
		;;
	--)
		shift
		break
		;;
	*) die "unknown option: $1 (use --help)" ;;
	esac
done

case "$non_interactive" in
0 | 1) ;;
*) die "LITEBOX_NON_INTERACTIVE must be 0 or 1" ;;
esac

if [ "$mode" = demo ]; then
	run_demo
else
	run_install
fi

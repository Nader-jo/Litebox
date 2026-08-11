#!/bin/sh
set -eu

# Keep the fixture independent from development-container defaults and the
# contributor's shell. Every installer input used below is declared explicitly.
unset APP_BASE_URL

repo_dir=$(CDPATH='' cd -- "$(dirname "$0")/.." && pwd)
test_dir=$(mktemp -d)
cleanup() {
	rm -rf "$test_dir"
}
trap cleanup 0

fixture="$test_dir/fixture"
mock_bin="$test_dir/bin"
install_dir="$test_dir/install"
mkdir -p "$fixture/bundle" "$mock_bin"
cp "$repo_dir/compose.yaml" "$repo_dir/Caddyfile" "$repo_dir/.env.example" \
	"$repo_dir/LICENSE" "$repo_dir/NOTICE" "$repo_dir/setup.sh" "$fixture/bundle/"
cp "$repo_dir/docs/DEPLOYMENT.md" "$fixture/bundle/DEPLOYMENT.md"
cp "$repo_dir/docs/UPGRADING.md" "$fixture/bundle/UPGRADING.md"
cp "$repo_dir/docs/BACKUP_AND_RESTORE.md" "$fixture/bundle/BACKUP_AND_RESTORE.md"
cp "$repo_dir/docs/USER_GUIDE.md" "$fixture/bundle/USER_GUIDE.md"
sed -i 's|^LITEBOX_IMAGE=.*|LITEBOX_IMAGE=ghcr.io/nader-jo/litebox:9.8.7|' "$fixture/bundle/.env.example"
tar -czf "$fixture/litebox_9.8.7_vps.tar.gz" -C "$fixture/bundle" .
(cd "$fixture" && sha256sum litebox_9.8.7_vps.tar.gz >litebox_9.8.7_vps.tar.gz.sha256)

cat >"$mock_bin/curl" <<'EOF'
#!/bin/sh
set -eu
destination=
url=
while [ "$#" -gt 0 ]; do
	case "$1" in
	--output)
		destination=$2
		shift 2
		;;
	--connect-timeout | --retry)
		shift 2
		;;
	--fail | --silent | --show-error | --location)
		shift
		;;
	*)
		url=$1
		shift
		;;
	esac
done
[ -n "$destination" ] || exit 1
cp "$TEST_FIXTURE_DIR/${url##*/}" "$destination"
EOF

cat >"$mock_bin/docker" <<'EOF'
#!/bin/sh
set -eu
printf '%s\n' "$*" >>"$TEST_DOCKER_LOG"
case "$*" in
"compose version" | "info" | "compose config --quiet") exit 0 ;;
*) exit 0 ;;
esac
EOF
chmod +x "$mock_bin/curl" "$mock_bin/docker"

output=$(PATH="$mock_bin:$PATH" \
	TEST_FIXTURE_DIR="$fixture" \
	TEST_DOCKER_LOG="$test_dir/docker.log" \
	LITEBOX_RELEASE_BASE_URL=https://releases.example.test \
	LITEBOX_VERSION=9.8.7 \
	LITEBOX_HOST=mail.example.test \
	MAILBOX_PRIMARY_ADDRESS=owner@example.test \
	MAILBOX_DISPLAY_NAME="Example Team" \
	RESEND_API_KEY=re_private_test \
	RESEND_WEBHOOK_SECRET=whsec_private_test \
	LITEBOX_USE_CADDY=0 \
	sh "$repo_dir/setup.sh" --non-interactive --no-start --install-dir "$install_dir")

grep -q '^LITEBOX_IMAGE=ghcr.io/nader-jo/litebox:9.8.7$' "$install_dir/.env"
grep -q '^APP_BASE_URL=https://mail.example.test$' "$install_dir/.env"
grep -q '^MAILBOX_PRIMARY_ADDRESS=owner@example.test$' "$install_dir/.env"
grep -q '^MAILBOX_DISPLAY_NAME=Example Team$' "$install_dir/.env"
grep -q '^LITEBOX_USE_CADDY=0$' "$install_dir/.env"
[ -f "$install_dir/UPGRADING.md" ]
[ -f "$install_dir/USER_GUIDE.md" ]
grep -q '^compose config --quiet$' "$test_dir/docker.log"
case "$output" in
*re_private_test* | *whsec_private_test*)
	echo "setup output exposed a secret" >&2
	exit 1
	;;
esac
case "$(uname -s)" in
MINGW* | MSYS* | CYGWIN*) ;;
*) [ "$(stat -c '%a' "$install_dir/.env")" = 600 ] ;;
esac

sed -i 's/^APP_LOG_LEVEL=.*/APP_LOG_LEVEL=debug/' "$install_dir/.env"
PATH="$mock_bin:$PATH" \
	TEST_FIXTURE_DIR="$fixture" \
	TEST_DOCKER_LOG="$test_dir/docker.log" \
	LITEBOX_RELEASE_BASE_URL=https://releases.example.test \
	LITEBOX_VERSION=9.8.7 \
	sh "$repo_dir/setup.sh" --non-interactive --no-start --install-dir "$install_dir" >/dev/null
grep -q '^APP_LOG_LEVEL=debug$' "$install_dir/.env"
grep -q '^RESEND_API_KEY=re_private_test$' "$install_dir/.env"

printf 'tampered' >>"$fixture/litebox_9.8.7_vps.tar.gz"
if PATH="$mock_bin:$PATH" \
	TEST_FIXTURE_DIR="$fixture" \
	TEST_DOCKER_LOG="$test_dir/docker.log" \
	LITEBOX_RELEASE_BASE_URL=https://releases.example.test \
	LITEBOX_VERSION=9.8.7 \
	LITEBOX_HOST=mail.example.test \
	MAILBOX_PRIMARY_ADDRESS=owner@example.test \
	RESEND_API_KEY=re_private_test \
	RESEND_WEBHOOK_SECRET=whsec_private_test \
	sh "$repo_dir/setup.sh" --non-interactive --no-start \
		--install-dir "$test_dir/tampered-install" >"$test_dir/tampered.log" 2>&1; then
	echo "setup accepted a bundle with an invalid checksum" >&2
	exit 1
fi
grep -q 'checksum verification failed' "$test_dir/tampered.log"

printf '%s\n' "setup.sh tests passed"

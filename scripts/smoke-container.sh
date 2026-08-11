#!/bin/sh
set -eu

image=${1:?usage: smoke-container.sh IMAGE [PLATFORM]}
platform=${2:-}
suffix="$$-$(date +%s)"
container="litebox-smoke-$suffix"
volume="litebox-smoke-$suffix"

cleanup() {
	docker rm -f "$container" >/dev/null 2>&1 || true
	docker volume rm "$volume" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

run_for_platform() {
	if [ -n "$platform" ]; then
		docker run --platform "$platform" "$@"
	else
		docker run "$@"
	fi
}

case "$image" in
*@sha256:*)
	# A previous platform pull can leave this multi-platform digest present in the
	# local image store. Pull the requested platform explicitly every time.
	if [ -n "$platform" ]; then
		docker pull --platform "$platform" "$image" >/dev/null
	else
		docker pull "$image" >/dev/null
	fi
	;;
*)
	if ! docker image inspect "$image" >/dev/null 2>&1; then
		if [ -n "$platform" ]; then
			docker pull --platform "$platform" "$image" >/dev/null
		else
			docker pull "$image" >/dev/null
		fi
	fi
	;;
esac

if [ -n "$platform" ]; then
	configured_platform=$(docker image inspect --format '{{.Os}}/{{.Architecture}}' "$image")
	if [ "$configured_platform" != "$platform" ]; then
		echo "expected image platform $platform, got $configured_platform" >&2
		exit 1
	fi
fi

configured_user=$(docker image inspect --format '{{.Config.User}}' "$image")
if [ "$configured_user" != "10001:10001" ]; then
	echo "expected image user 10001:10001, got $configured_user" >&2
	exit 1
fi

run_for_platform --rm "$image" version

run_for_platform \
	--detach \
	--name "$container" \
	--read-only \
	--tmpfs /tmp:size=64m,mode=1777 \
	--cap-drop ALL \
	--security-opt no-new-privileges:true \
	--env APP_ENV=development \
	--env APP_BASE_URL=http://localhost:8080 \
	--env APP_LISTEN_ADDR=:8080 \
	--env MAILBOX_PRIMARY_ADDRESS=hello@example.test \
	--env MAILBOX_DISPLAY_NAME="Litebox smoke test" \
	--env MAILBOX_ALLOWED_RECIPIENTS=hello@example.test \
	--volume "$volume:/data" \
	"$image" >/dev/null

attempt=0
while [ "$attempt" -lt 45 ]; do
	status=$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}missing{{end}}' "$container")
	case "$status" in
		healthy)
			echo "$image ($platform) is healthy"
			exit 0
			;;
		unhealthy|missing)
			docker logs "$container" >&2
			echo "container health status: $status" >&2
			exit 1
			;;
	esac
	attempt=$((attempt + 1))
	sleep 1
done

docker logs "$container" >&2
echo "container did not become healthy within 45 seconds" >&2
exit 1

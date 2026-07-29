#!/usr/bin/env bash
# Gather every fact the rune-authoring prompt asks for, in one pass.
# Usage:  ./rune-facts.sh <image> [config-path-inside-image]
# Example: ./rune-facts.sh eclipse-mosquitto:latest /mosquitto/config/mosquitto.conf
#
# Paste the whole output back to the model. It is written to be pasteable:
# clearly delimited, no colour, no interactive prompts.
set -u
IMG="${1:?usage: rune-facts.sh <image> [config-path]}"
CFG="${2:-}"
D=docker
command -v docker >/dev/null || { echo "docker not found"; exit 1; }
# Use sudo only if the current user cannot talk to docker.
$D info >/dev/null 2>&1 || D="sudo docker"

echo "=============== FACT 1: image exists ==============="
$D pull "$IMG" 2>&1 | tail -3

echo
echo "=============== FACT 2: entrypoint / cmd / user ==============="
$D inspect "$IMG" --format 'Entrypoint={{json .Config.Entrypoint}}
Cmd={{json .Config.Cmd}}
User={{printf "%q" .Config.User}}
ExposedPorts={{json .Config.ExposedPorts}}
Volumes={{json .Config.Volumes}}
Env={{json .Config.Env}}' 2>&1

echo
echo "=============== FACT 3: real startup log (15s) ==============="
CN="runefacts-$$"
$D rm -f "$CN" >/dev/null 2>&1
if $D run -d --name "$CN" "$IMG" >/dev/null 2>&1; then
  sleep 15
  $D logs "$CN" 2>&1 | head -60
  STATUS=$($D ps --filter "name=$CN" --format '{{.Status}}' 2>/dev/null | head -1)
  echo "--- after 15s: ${STATUS:-EXITED — the app did not stay up, see the log above} ---"
else
  echo "(could not start the container at all)"
fi

echo
echo "=============== FACT 4: candidate state directories ==============="
$D exec "$CN" sh -c 'ls -la /' 2>/dev/null || $D run --rm --entrypoint sh "$IMG" -c 'ls -la /' 2>&1 | head -30
if [ -n "${VOLDIRS:-}" ]; then :; fi

echo
echo "=============== FACT 5: what the app runs as ==============="
$D exec "$CN" id 2>/dev/null || $D run --rm --entrypoint sh "$IMG" -c 'id' 2>&1 | head -3
echo "--- processes actually running ---"
$D exec "$CN" ps -eo user,args 2>/dev/null | head -8 || echo "(ps unavailable)"

echo
echo "=============== FACT 6: shipped default config ==============="
if [ -n "$CFG" ]; then
  $D exec "$CN" sh -c "cat '$CFG'" 2>/dev/null \
    || $D run --rm --entrypoint sh "$IMG" -c "cat '$CFG'" 2>&1 | head -40
else
  echo "(no config path given — re-run with the path as the 2nd argument once FACT 4 shows where it lives)"
fi

echo
echo "=============== entrypoint script (often decides ownership) ==============="
for f in /docker-entrypoint.sh /entrypoint.sh /init /usr/local/bin/docker-entrypoint.sh; do
  $D exec "$CN" sh -c "test -f $f && echo '--- $f ---' && head -40 $f" 2>/dev/null && break
done

$D rm -f "$CN" >/dev/null 2>&1
echo
echo "=============== done — paste everything above ==============="

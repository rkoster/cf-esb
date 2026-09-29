#!/usr/bin/env bash
set -euo pipefail

if [[ ! -f .secrets ]]; then
  printf '%s\n' 'Missing .secrets. Create it from the local Cloud Foundry vars.' >&2
  exit 1
fi

if ! command -v cf >/dev/null 2>&1; then
  printf '%s\n' 'cf CLI is required for deploy.' >&2
  exit 1
fi

eval "$(ruby -ryaml -rshellwords -e 'YAML.load_file(".secrets").each { |key, value| puts "export #{key.upcase}=#{Shellwords.escape(value.to_s)}" }')"

: "${CF_ORG:?CF_ORG is required in .secrets}"
: "${CF_SPACE:?CF_SPACE is required in .secrets}"
: "${CF_INSTANCE_SPACE:?CF_INSTANCE_SPACE is required in .secrets}"
if [[ -z "${CF_INSTANCE_SPACE_GUID:-}" ]]; then
  CF_INSTANCE_SPACE_GUID="$(cf space "$CF_INSTANCE_SPACE" --guid)"
  export CF_INSTANCE_SPACE_GUID
fi
: "${BROKER_USERNAME:?BROKER_USERNAME is required in .secrets}"
: "${BROKER_PASSWORD:?BROKER_PASSWORD is required in .secrets}"
: "${BROKER_URL:?BROKER_URL is required in .secrets}"
BROKER_NAME="${BROKER_NAME:-cf-esb}"

cf target -o "$CF_ORG" -s "$CF_SPACE"
cf space "$CF_INSTANCE_SPACE" >/dev/null 2>&1 || cf create-space "$CF_INSTANCE_SPACE" -o "$CF_ORG"

# cf push prints the manifest's resolved environment variables. Redact the
# credential fields while preserving the command's exit status via pipefail.
cf push cf-esb -f manifest.yml --vars-file .secrets 2>&1 \
  | sed -E '/(CF_CLIENT_SECRET|BROKER_PASSWORD):/s/:.*/: [redacted]/'

# --update-if-exists makes registration safe to repeat after every push.
CF_BROKER_PASSWORD="$BROKER_PASSWORD" \
  cf create-service-broker --update-if-exists "$BROKER_NAME" "$BROKER_USERNAME" "$BROKER_URL"

# Keep all catalog offerings enabled for the configured org. The offering
# names come from the packaged service YAML so adding an offering also enables
# its marketplace access on the next deploy.
while IFS= read -r offering; do
  [[ -n "$offering" ]] || continue
  if ! cf service-access | awk -v offering="$offering" -v org="$CF_ORG" \
    '$1 == offering && ($3 == "all" || ($3 == "limited" && $4 == org)) { found=1 } END { exit !found }'; then
    cf enable-service-access "$offering" -b "$BROKER_NAME" -o "$CF_ORG"
  fi
done < <(ruby -ryaml -e 'YAML.load_file("config/services.yml").fetch("services").each { |service| puts service.fetch("name") }')

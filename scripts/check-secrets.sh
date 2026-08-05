#!/usr/bin/env bash
# Fail if a plaintext credential was committed.
#
# Scope is deliberately narrow: only files git tracks, only shapes that are
# unambiguously keys. A scanner that cries wolf gets bypassed, and a bypassed
# scanner protects nothing. Detecting a leak after the fact is also the last
# line of defence, not the first — the design goal is that no code path ever
# writes a key to a file in the first place.
#
# Usage: scripts/check-secrets.sh [path ...]

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

if ! command -v git >/dev/null 2>&1; then
  echo "check-secrets: git is required" >&2
  exit 2
fi

# Tracked files only. Generated output and local config are untracked by
# design, and scanning them would flag the developer's own working key.
#
# Read with a NUL delimiter rather than mapfile, which macOS's bash 3.2 does
# not have. This script has to run on the developer's machine, not just in CI.
files=()
while IFS= read -r -d '' file; do
  files+=("$file")
done < <(git ls-files -z "$@")

if [[ ${#files[@]} -eq 0 ]]; then
  echo "check-secrets: no tracked files to scan"
  exit 0
fi

# Files that must be exempt, with the reason each one is safe:
#   this script          - it contains the patterns themselves
#   *_test.go            - fixtures use obvious fakes to assert non-leakage
#   internal/webui/dist  - generated build output, not source
#   package-lock.json    - integrity hashes look like base64 secrets
exempt() {
  case "$1" in
    scripts/check-secrets.sh|*_test.go|internal/webui/dist/*|*/package-lock.json|package-lock.json) return 0 ;;
    *) return 1 ;;
  esac
}

# Each rule is "name<TAB>extended regex". Prefixed vendor keys are matched by
# their published shape; the generic rules catch a key assigned to an
# obviously-named field.
rules=(
$'openai key\t(sk|sk-proj|sk-svcacct)-[A-Za-z0-9_-]{20,}'
$'anthropic key\tsk-ant-[A-Za-z0-9_-]{20,}'
$'google key\tAIza[0-9A-Za-z_-]{30,}'
$'aws access key id\t(A3T[A-Z0-9]|AKIA|ASIA|ABIA|ACCA)[A-Z0-9]{16}'
$'slack token\txox[baprs]-[A-Za-z0-9-]{10,}'
$'github token\tgh[pousr]_[A-Za-z0-9]{30,}'
$'private key block\t-----BEGIN [A-Z ]*PRIVATE KEY-----'
$'assigned api key\t(api_?key|apikey|secret_?key|access_?token|password)["\x27]?[[:space:]]*[:=][[:space:]]*["\x27][^"\x27$\{<[:space:]]{12,}'
)

found=0
for file in "${files[@]}"; do
  [[ -f "$file" ]] || continue
  exempt "$file" && continue
  # Skip binaries: grep on them is noise, and a key hidden in one is beyond
  # what a shell scanner can honestly claim to catch.
  grep -Iq . "$file" 2>/dev/null || continue

  for rule in "${rules[@]}"; do
    name=${rule%%$'\t'*}
    pattern=${rule#*$'\t'}

    # Report the location and the rule, never the matched text: printing the
    # secret to CI logs would turn the check into a second leak.
    while IFS=: read -r lineno _; do
      [[ -n "$lineno" ]] || continue
      echo "$file:$lineno: possible $name" >&2
      found=1
    done < <(grep -nE "$pattern" "$file" 2>/dev/null || true)
  done
done

if [[ $found -ne 0 ]]; then
  cat >&2 <<'EOF'

check-secrets: plaintext credentials found in tracked files.

Remove the value from the file and store it properly:

  vs credential set <provider>

If the value is a placeholder or a test fixture, make that obvious: use an
${ENV_VAR} reference, or a value short enough not to look like a real key.
Rewriting history is not enough on its own — treat any key that reached a
commit as compromised and rotate it. See doc/arch/credentials.md.
EOF
  exit 1
fi

echo "check-secrets: no plaintext credentials in ${#files[@]} tracked files"

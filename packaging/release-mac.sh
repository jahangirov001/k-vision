#!/usr/bin/env bash
# Builds the macOS app on this Mac and attaches it to the GitLab release of the
# current version -- the one part of a release the pipeline can't build (it needs
# the macOS SDK). No git commit is involved: the files go to the release, like the
# Windows and Linux ones the pipeline uploads.
#
#   GITLAB_TOKEN=glpat-... make release-mac            build + upload
#   GITLAB_TOKEN=glpat-... make release-mac DRY=1      show what would happen
#
# The token needs the "api" scope (Developer role or higher on the project).
# Run it after the tag's pipeline has created the release.
set -euo pipefail
cd "$(dirname "$0")/.."

PROJECT="${GITLAB_PROJECT:-FrogoAI/Infra/Tools/k-vision}"
API="${GITLAB_API:-https://gitlab.com/api/v4}"
VERSION="$(cat internal/version/VERSION)"
TAG="${TAG:-$VERSION}"
ENC_PROJECT="$(printf %s "$PROJECT" | sed 's|/|%2F|g')"
PKG_URL="$API/projects/$ENC_PROJECT/packages/generic/k-vision/$TAG"
DRY="${DRY:-}"

say() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
die() { printf '\033[1;31mError:\033[0m %s\n' "$*" >&2; exit 1; }

[ "$(uname -s)" = Darwin ] || die "the Mac app can only be built on a Mac"
[ -n "${GITLAB_TOKEN:-}" ] || [ -n "$DRY" ] || die "set GITLAB_TOKEN (a personal access token with the api scope)"

# The release must be made from the same code: the tag has to be checked out here.
if [ -z "$DRY" ]; then
  git fetch -q --tags origin
  [ "$(git rev-parse HEAD)" = "$(git rev-parse "$TAG^{commit}" 2>/dev/null || echo none)" ] \
    || die "HEAD is not tag $TAG. Run: git checkout $TAG   (then this again)"
  [ -z "$(git status --porcelain -- . ':!dist')" ] || die "uncommitted changes; the Mac app must match the tag exactly"
fi

say "Building K-Vision $VERSION for macOS (Apple silicon + Intel)"
make dist >/dev/null 2>&1 || make dist
FILES=("dist/K-Vision-$VERSION-macos.zip" "dist/K-Vision-$VERSION-macos.dmg")
for f in "${FILES[@]}"; do [ -f "$f" ] || die "missing $f after make dist"; done

auth=(--header "PRIVATE-TOKEN: ${GITLAB_TOKEN:-dry-run}")
for f in "${FILES[@]}"; do
  n="$(basename "$f")"
  if [ -n "$DRY" ]; then echo "would upload $n -> $PKG_URL/$n"; continue; fi
  say "Uploading $n"
  curl -sSf "${auth[@]}" --upload-file "$f" "$PKG_URL/$n" >/dev/null
  # Add (or replace) the release's link to it.
  existing="$(curl -sSf "${auth[@]}" "$API/projects/$ENC_PROJECT/releases/$TAG/assets/links" \
    | python3 -c 'import json,sys; n=sys.argv[1]; print(next((str(l["id"]) for l in json.load(sys.stdin) if l["name"]==n), ""))' "$n")"
  [ -n "$existing" ] && curl -sSf -X DELETE "${auth[@]}" "$API/projects/$ENC_PROJECT/releases/$TAG/assets/links/$existing" >/dev/null
  curl -sSf -X POST "${auth[@]}" "$API/projects/$ENC_PROJECT/releases/$TAG/assets/links" \
    --data-urlencode "name=$n" --data-urlencode "url=$PKG_URL/$n" --data-urlencode "link_type=package" >/dev/null
done

[ -n "$DRY" ] || say "Done: the macOS files are on the $TAG release (Deploy -> Releases)."

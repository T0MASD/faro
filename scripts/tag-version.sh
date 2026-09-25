#!/usr/bin/env bash
#
# Create the next semantic version tag and push it. Pushing a v* tag is what
# triggers .github/workflows/release.yml, which runs the suite against a kinc
# cluster, publishes the GitHub release and pushes the operator image.
#
#   ./scripts/tag-version.sh patch|minor|major
#
# Set DRY_RUN=1 to print the tag that would be created and stop.

set -euo pipefail

bump="${1:-}"
case "$bump" in
    patch|minor|major) ;;
    *)
        echo "usage: $0 {patch|minor|major}" >&2
        exit 1
        ;;
esac

# A tag names a commit, so it has to name one that is pushed and reproducible.
branch=$(git rev-parse --abbrev-ref HEAD)
if [ "$branch" != "main" ]; then
    echo "❌ On '$branch'. Release tags are cut from main." >&2
    exit 1
fi

if [ -n "$(git status --porcelain)" ]; then
    echo "❌ Working tree has uncommitted changes." >&2
    git status --short >&2
    exit 1
fi

git fetch --quiet --tags origin

if [ "$(git rev-parse HEAD)" != "$(git rev-parse origin/main)" ]; then
    echo "❌ main and origin/main differ. Pull or push first." >&2
    exit 1
fi

# Highest existing vMAJOR.MINOR.PATCH, ignoring any other tag shape.
latest=$(git tag --list 'v[0-9]*.[0-9]*.[0-9]*' --sort=-v:refname | head -1)
latest="${latest:-v0.0.0}"

IFS=. read -r major minor patch <<< "${latest#v}"
case "$bump" in
    major) major=$((major + 1)); minor=0; patch=0 ;;
    minor) minor=$((minor + 1)); patch=0 ;;
    patch) patch=$((patch + 1)) ;;
esac
next="v${major}.${minor}.${patch}"

if git rev-parse -q --verify "refs/tags/${next}" >/dev/null; then
    echo "❌ ${next} already exists." >&2
    exit 1
fi

echo "Latest tag : ${latest}"
echo "Next tag   : ${next} (${bump})"
echo "Commit     : $(git log -1 --oneline)"

if [ "${DRY_RUN:-0}" = "1" ]; then
    echo "DRY_RUN=1, stopping before tagging."
    exit 0
fi

# Annotated, so the tag carries its own message and date.
git tag -a "${next}" -m "faro ${next}

$(git log --pretty=format:'  %s' "${latest}..HEAD" | grep -vE '^  Merge pull request' || true)"

git push origin "${next}"

echo "✅ Pushed ${next}. Release workflow: https://github.com/T0MASD/faro/actions/workflows/release.yml"

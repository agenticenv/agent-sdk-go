#!/usr/bin/env bash
# release.sh — create a release tag (triggers GitHub Actions release workflow)
#
# Usage:
#   ./scripts/release.sh              # Auto-increment patch from latest tag (v0.0.1 → v0.0.2)
#   ./scripts/release.sh v1.0.0       # Use exact version (v1.0.0 or 1.0.0)
#   ./scripts/release.sh v1.0.0 -p   # Create tag and push (triggers release)
#
# Tag must be pushed to run the release pipeline: git push origin <tag>

set -e

PUSH=false
VERSION=""

# Parse args
while [[ $# -gt 0 ]]; do
  case $1 in
    -p|--push)
      PUSH=true
      shift
      ;;
    v*)
      VERSION="$1"
      shift
      ;;
    [0-9]*)
      VERSION="v$1"
      shift
      ;;
    *)
      echo "Unknown option: $1"
      exit 1
      ;;
  esac
done

# Find project root (dir containing go.mod)
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
if [[ ! -f go.mod ]]; then
  echo "Error: go.mod not found"
  exit 1
fi

# Fetch latest tags
git fetch --tags 2>/dev/null || true

# Resolve version
if [[ -z "$VERSION" ]]; then
  LATEST=$(git describe --tags --abbrev=0 2>/dev/null || echo "v0.0.0")
  LATEST=${LATEST#v}
  IFS=. read -r MAJOR MINOR PATCH <<< "$LATEST"
  PATCH=$((PATCH + 1))
  VERSION="v${MAJOR}.${MINOR}.${PATCH}"
  echo "Auto-incremented from $LATEST → $VERSION"
else
  [[ "$VERSION" != v* ]] && VERSION="v$VERSION"
fi

# Check tag doesn't exist
if git rev-parse "$VERSION" &>/dev/null; then
  echo "Error: tag $VERSION already exists"
  exit 1
fi

# Create tag
git tag "$VERSION"
echo "Created tag: $VERSION"

if $PUSH; then
  git push origin "$VERSION"
  echo "Pushed $VERSION — release workflow will run"
else
  echo "Push to trigger release: git push origin $VERSION"
fi

# Releasing

This document is for **maintainers** who cut releases. The release workflow runs **only when a tag is pushed** — no tag, no release.

## Who can release

Only users with **push access** to the repository can create tags and trigger releases. Typically repo maintainers and owners. Contributors without push access cannot create tags.

## How it works

1. **You create and push a tag** (e.g. `v0.0.1`, `v1.0.0`, `v2.0.3`)
2. **GitHub Actions runs** the Release workflow
3. **Builds** `agentctl` for Linux amd64
4. **Creates a GitHub Release** with the tag and attaches `agentctl-{version}-linux-amd64.tar.gz`

## Checklist before tagging

- [ ] CI is green on `main` (lint, test, build pass — see [Actions](https://github.com/vvsynapse/temporal-agent-sdk-go/actions))
- [ ] `make lint` and `make test` pass locally (or rely on CI)
- [ ] CHANGELOG or release notes updated (if you maintain one)
- [ ] Version follows [semver](https://semver.org):
  - **Patch** (0.0.1 → 0.0.2): bug fixes, no API changes
  - **Minor** (0.1.0 → 0.2.0): new features, backward compatible
  - **Major** (1.0.0 → 2.0.0): breaking changes

## Creating a release

### Option 1: Use the release script

```bash
# From project root
./scripts/release.sh              # Auto-increment patch (v0.0.1 → v0.0.2)
./scripts/release.sh v1.0.0      # Use exact version
./scripts/release.sh v1.0.0 -p   # Create tag and push (triggers release)
```

### Option 2: Manual tag

```bash
git checkout main
git pull origin main

git tag v0.0.1
git push origin v0.0.1
```

The workflow runs automatically when the tag is pushed. Check [Actions](https://github.com/vvsynapse/temporal-agent-sdk-go/actions) for status.

## Version examples

| Tag     | Use case                     |
|---------|------------------------------|
| v0.0.1  | First pre-release            |
| v0.0.2  | Next patch in 0.0.x          |
| v1.0.0  | First stable / public release |
| v1.0.1  | Patch for 1.0                |
| v2.0.0  | Major breaking release       |

Any valid semver tag works: `v0.0.1`, `v1.0.0`, `v2.0.3`, etc.

## Notes

- **Tag = release.** Pushing a tag immediately triggers the build and creates the release. Double-check the version before pushing.
- **Tags are immutable.** If you push `v0.0.1` by mistake, you must create a new tag (e.g. `v0.0.2`) — you cannot change or delete the release tag easily.
- **Go modules:** The tag becomes the module version for `go get github.com/vvsynapse/temporal-agent-sdk-go@v1.0.0`.

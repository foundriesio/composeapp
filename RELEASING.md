# RELEASING

This project publishes GitHub Releases containing:

- Linux binaries (amd64, arm64)
- Debian `.deb` packages (amd64, arm64)

A release is created automatically by GitHub Actions when a tag matching `v*` is pushed.

The semantic versioning is applied, i.e. it follows SemVer `(vMAJOR.MINOR.PATCH)` format.

---

## Release Steps

### 1) Create a branch for the release changelog

```bash
git checkout main
git pull
git checkout -b changelog/1.1.0
```

### Update debian/changelog

```bash
make release-prep VERSION=1.1.0 # version without "v" prefix
```

### Update debian/changelog

* It must not be UNRELEASED, set `unstable` by default.
* If needed, edit debian/changelog manually.

### Build and test locally

```bash
make deb
make deb-lint
make deb-test
```

### Commit And Open a PR

```bash
git add debian/changelog
git commit -m "debian: release 1.1.0"
git push -u origin changelog/v1.1.0
```

Open a PR and merge it into main after review and approval.

### Tag Release

After the PR is merged:

```bash
git checkout main
git pull
git tag v1.1.0
git push origin v1.1.0
```

This triggers GitHub Actions to build artifacts and create the GitHub Release:

* Linux binaries for amd64 and arm64.
* Debian artifacts (`"deb"`) for amd64 and arm64.
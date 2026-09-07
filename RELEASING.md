# Releasing Hop

## Local validation

Go 1.24+, Python 3, and a local OpenSSH SFTP server are required for the full test suite.
On Debian/Ubuntu install `openssh-sftp-server` and `python3`; macOS includes the server.
Tests do not contact SSH machines. Run them as a normal user so permission checks work.

```sh
make check
make dist
```

`dist/<version>/` contains four tarballs and `SHA256SUMS`. Each tarball contains the
`hop` executable, README, changelog, and LICENSE if present. Python is only a build/test
dependency; installed binaries require OpenSSH and a terminal, with no Go runtime.

## First GitHub release

1. Choose the GitHub repository and distribution license before publishing.
2. Add that repository as `origin`, then push `main`.
3. Check `VERSION` and `CHANGELOG.md`, and ensure CI passes.
4. Create and push the annotated version tag:

   ```sh
   git tag -a "v$(cat VERSION)" -m "Hop $(cat VERSION)"
   git push origin "v$(cat VERSION)"
   ```

The Release workflow tests on macOS and Linux, builds all four architectures with CGO
disabled, verifies checksums, smoke-tests Linux AMD64, and publishes the GitHub release.
The tag must match `VERSION`. The workflow uses the built-in `GITHUB_TOKEN`; no personal
access token or signing secret is required. Repository settings must permit Actions.
The publish job alone requests `contents: write`.

A failed upload can leave a draft; rerunning resumes that draft. Already-published
releases are never overwritten. For later releases update VERSION and CHANGELOG in a
new commit, then push a new tag. Edit the release notes step to use just the new entry
as the changelog grows.

macOS builds are not Developer ID signed or notarized. No signing credentials are
configured. Keep downloadable builds and checksums out of Git; Actions attaches them
as release assets. Linux binaries are built without a libc dependency.

GitHub Actions references: [checkout](https://github.com/actions/checkout),
[setup-go](https://github.com/actions/setup-go),
[release CLI](https://cli.github.com/manual/gh_release_create).

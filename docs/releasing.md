# Releasing Hop

Run the commands below from the repository root.

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

## GitHub releases

1. Select the distribution license before publishing.
2. Use `https://github.com/makuga01/hop.git` as `origin`, then push `main` after approval.
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
new commit, then push a new tag. The workflow uses the first version entry in
CHANGELOG.md as the release notes.

macOS builds are not Developer ID signed or notarized. No signing credentials are
configured. Keep downloadable builds and checksums out of Git; Actions attaches them
as release assets. Linux binaries are built without a libc dependency.

GitHub Actions references: [checkout](https://github.com/actions/checkout),
[setup-go](https://github.com/actions/setup-go),
[release CLI](https://cli.github.com/manual/gh_release_create).

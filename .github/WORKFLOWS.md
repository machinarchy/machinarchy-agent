# GitHub Actions

| Workflow | Runs on | Does |
|---|---|---|
| `ci.yml` | pull requests; pushes to `main`, `beta`, `alpha` | `verify` (build, vet, lint, race tests, coverage, deployment checks), `security` (govulncheck on both modules, short fuzz of the protocol decoders), then `orchestrate-release` on the three branches |
| `release.yml` | called by `ci.yml` after the checks pass | Release Please for the branch's channel; on a new release builds the Linux archives, uploads them with `checksums.txt` and attests them |
| `docker.yml` | pull requests touching the image, pushes to `main`, tags `v*` | builds the image (PRs), pushes `sha-…` images from `main` and the versioned image from a release tag; only a stable tag moves `latest` |

Every action is pinned by commit SHA (the tag is in a comment); Dependabot
proposes bumps. golangci-lint includes the gosec rules, so there is no
separate gosec step.

## Required repository secret

`MY_GITHUB_TOKEN`: a fine-grained PAT (or GitHub App token) for this
repository with Contents, Issues and Pull requests: read and write. Release
Please uses it to open and update release PRs and create tags, and `gh` uses
it to upload the archives. The tag it creates must start `docker.yml`, which
events made with the default `GITHUB_TOKEN` would not.

## Release channels

The branch you merge into decides what is released:

| Branch | Channel | Version | GitHub Release | Image tags |
|---|---|---|---|---|
| `main` | stable | `v0.0.1` | latest release | `0.0.1`, `latest` |
| `beta` | beta | `v0.0.1-beta.1`, `-beta.2` … | pre-release | `0.0.1-beta.1` |
| `alpha` | alpha | `v0.0.1-alpha.1`, `-alpha.2` … | pre-release | `0.0.1-alpha.1` |

Release Please computes the next version from Conventional Commits on the
branch (`feat:` minor, `fix:` patch, `!` major; below 1.0 a breaking change
raises the minor) and adds the `-beta.N` / `-alpha.N` suffix on the
pre-release branches. Nobody types a version or a tag. The first release of
each channel is `0.0.1` (`initial-version`; the manifests start empty).

1. Merge feature PRs (squash, Conventional Commit title) into `alpha`.
2. Release Please opens "chore(alpha): release 0.0.1-alpha.1". Merging it
   creates the tag and the pre-release; the workflow adds the `linux/amd64`
   and `linux/arm64` archives, checksums and attestations; the tag starts
   `docker.yml`.
3. Promote with merge commits: `alpha` into `beta`, then `beta` into `main`.
4. Fixes found in a beta go to `beta` and are merged back to `alpha`.

Per channel, `.github/release-please/config.<channel>.json` and
`manifest.<channel>.json` (the last released version) are separate, so
merging one branch into the next never conflicts. `CHANGELOG.md` is written by
hand (`skip-changelog`); the notes on the GitHub Release come from the commits.

**Enforced:** the release job refuses a tag whose suffix does not match its
branch; a pre-release never gets `latest`; the CI run of a push is never
cancelled (only pull request runs are), so a release is not cut off.

**Set up once:** create `beta` and `alpha` from `main`, protect all three
(required checks `verify` and `security`, no force pushes). The `protocol`
module is tagged by hand as `protocol/v0.1.0` (the `v*` triggers do not match
it).

Not yet exercised: the workflows have never run on GitHub, so watch the first
release of each channel.

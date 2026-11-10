# Releases and updates

Every push to `main` runs `.github/workflows/release.yml` and publishes a GitHub **prerelease** `v0.1.N` (N = the workflow run number, also the app build number). The release marked "latest" is the newest build promoted to stable.

Assets: `HAL9001-0.1.N.dmg`, `HAL9001-0.1.N.app.zip`, `SHA256SUMS`, and `update.json` (the signed beta manifest, when signing is configured). All assets are attached in a single `gh release create`, so the release is complete when it is published.

## Update channels

The app updater reads one manifest per channel, served by GitHub Pages from the `gh-pages` branch:

- `https://adamveld12.github.io/hal9001/stable.json`
- `https://adamveld12.github.io/hal9001/beta.json`

- **beta**: every build from `main` (`release.yml` writes `beta.json`, commit message `beta: 0.1.N`).
- **stable**: builds promoted with a `stable/v0.1.N` tag (`promote.yml` writes `stable.json`, commit message `stable: 0.1.N`). Until the first promotion `stable.json` does not exist; the app treats the 404 as "no release on this channel yet" (up to date), not an error.

Why Pages and not a release: GitHub's release immutability locks a published release's assets and tag for good, and a locked release cannot be repaired or have its tag reused. A manifest that CI rewrites on every build cannot live in one. Branch files can always be updated, so the repository can keep "Enable release immutability" on for the `v0.1.N` releases.

`scripts/publish-feed.sh` does the publishing for both workflows. It creates `gh-pages` as an orphan branch (with `.nojekyll` and a small `index.html`) if it is missing, writes only its own channel's file, commits and pushes with the workflow's `GITHUB_TOKEN`. If the push is rejected because the other workflow pushed first, it refetches and tries again (up to 5 times); the two workflows write different files, so this needs no shared concurrency group. Pages serves files with a cache of about 10 minutes, so a new build can take that long to show up in an app's update check.

## Reruns and immutable releases

A rerun of a `release` workflow run reuses its run number, hence the same `v0.1.N` tag. `release.yml` handles that without touching the release:

- release `v0.1.N` does not exist: it is created with all assets.
- it exists and has the DMG, `.app.zip`, `SHA256SUMS` and (when signing is on) `update.json`: it is kept as is and the beta manifest is republished from that release's own `update.json` (a rebuilt zip is not byte-identical and would fail the signature and checksum).
- it exists but is incomplete: the run fails with an error. An immutable release cannot be repaired (assets cannot be added or replaced, the tag cannot be moved, and a deleted release's tag cannot be reused). Push a new commit to get the next build number.

Promotion (`promote.yml`) only edits a release's title, notes, pre-release flag and "latest" flag, which stay editable on immutable releases.

## One-time setup for the Pages feed

1. Merge the change that introduced `scripts/publish-feed.sh`, then let `release.yml` run on `main` once. It creates the `gh-pages` branch.
2. Settings → Pages → Build and deployment: Source "Deploy from a branch", branch `gh-pages`, folder `/ (root)`. Check `https://adamveld12.github.io/hal9001/beta.json` returns the manifest (first deploy takes a minute or two).
3. Settings → General → Releases: enable "Enable release immutability" again if it was turned off.
4. Optionally remove the old, locked `channels` release (its tag name can never be reused, which is fine): `gh release delete channels --cleanup-tag --yes`.

Apps built before the feed moved (v0.1.6 and v0.1.7) still read `https://github.com/adamveld12/hal9001/releases/download/channels/<channel>.json`, which never held a manifest, so they never see an update. Install the first build from `main` after this change by hand; later updates come through the feed.

## Promoting a build to stable

```sh
git tag stable/v0.1.42 v0.1.42 && git push origin stable/v0.1.42
```

(or run the `promote` workflow manually with `version` = `0.1.42`). `promote.yml` fails unless release `v0.1.42` exists, has `update.json`, and the tag points at the same commit. It does not rebuild: it downloads the release's `update.json` and zip, runs `hal9001-release promote` (verifies size, sha256 and the signature against `internal/update/publickey.txt`; needs no private key), writes `stable.json` (same manifest, channel `stable`, notes = commits since the previous `stable/v*` tag), publishes it to `gh-pages`, flips `v0.1.42` to a regular release marked latest and prepends "Promoted to stable on <date>" plus the stable notes to its body.

## Rolling back stable

The updater never downgrades (an update is offered only for a higher build number). Promoting an older build moves `stable.json` backwards, which stops further installs of the bad build but does not revert apps that already took it. To fix users, merge a fix to `main` and promote the newer build.

A manifest holds the version, build, commit, `.app.zip` URL, size, sha256 and an Ed25519 signature over the zip bytes. The app verifies the signature against the public key embedded from `internal/update/publickey.txt`.

## Signing key setup (once)

```sh
mise run update:keygen                                   # writes update-signing-key.txt and internal/update/publickey.txt
gh secret set HAL9001_UPDATE_SIGNING_KEY < update-signing-key.txt
git add internal/update/publickey.txt && git commit      # ship the public key
```

Then store the private key in a password manager and delete `update-signing-key.txt`. Without the secret, CI warns and publishes the release without manifests.

`hal9001-release manifest` fails if the secret's public key differs from `publickey.txt`.

## Rotation

Installed apps trust only the public key they shipped with. To rotate, release an app that embeds the new public key while still signing with the old key, wait for users to update, then switch the secret. Run `keygen --force` to replace `publickey.txt`.

# Releases and updates

Every push to `main` runs `.github/workflows/release.yml` and publishes a GitHub **prerelease** `v0.1.N` (N = the workflow run number, also the app build number). The release marked "latest" is the newest build promoted to stable.

Assets: `HAL9001-0.1.N.dmg`, `HAL9001-0.1.N.app.zip`, `SHA256SUMS`, and `update.json` (the signed beta manifest, when signing is configured).

## Update channels

The app updater reads one manifest per channel, hosted as assets of the fixed `channels` prerelease:

- `https://github.com/adamveld12/hal9001/releases/download/channels/stable.json`
- `https://github.com/adamveld12/hal9001/releases/download/channels/beta.json`

- **beta**: every build from `main` (`release.yml` writes `beta.json`).
- **stable**: builds promoted with a `stable/v0.1.N` tag (`promote.yml` writes `stable.json`). Until the first promotion `stable.json` does not exist; the app treats the 404 as "no release on this channel yet" (up to date), not an error.

## Promoting a build to stable

```sh
git tag stable/v0.1.42 v0.1.42 && git push origin stable/v0.1.42
```

(or run the `promote` workflow manually with `version` = `0.1.42`). `promote.yml` fails unless release `v0.1.42` exists, has `update.json`, and the tag points at the same commit. It does not rebuild: it downloads the release's `update.json` and zip, runs `hal9001-release promote` (verifies size, sha256 and the signature against `internal/update/publickey.txt`; needs no private key), writes `stable.json` (same manifest, channel `stable`, notes = commits since the previous `stable/v*` tag), uploads it to the `channels` release, flips `v0.1.42` to a regular release marked latest and prepends "Promoted to stable on <date>" plus the stable notes to its body.

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

# Releases and updates

Every push to `main` runs `.github/workflows/release.yml` and publishes a GitHub release `v0.1.N` (N = the workflow run number, also the app build number).

Assets: `HAL9001-0.1.N.dmg`, `HAL9001-0.1.N.app.zip`, `SHA256SUMS`, and `update.json` (the signed manifest, when signing is configured).

## Update channels

The app updater reads one manifest per channel, hosted as assets of the fixed `channels` prerelease:

- `https://github.com/adamveld12/hal9001/releases/download/channels/stable.json`
- `https://github.com/adamveld12/hal9001/releases/download/channels/beta.json`

Currently every main build updates both channels. The policy is the `CHANNELS` variable in the "Update manifest" workflow step.

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

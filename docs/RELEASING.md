# Releasing

## Cut a release

```sh
git tag v0.1.0 && git push origin v0.1.0
```

The `release` workflow then builds the server binaries and the signed APK, checks the APK's signing certificate, and publishes a GitHub release with everything attached. `install.sh` and the README link to `releases/latest`, so they pick it up automatically.

Tags must be `vMAJOR.MINOR.PATCH`, with MINOR and PATCH below 100. The Android `versionCode` is `MAJOR*10000 + MINOR*100 + PATCH` (v1.2.3 → 10203). Android only installs an update with a higher versionCode, so **never reuse or lower a tag**.

To test signing without publishing, run the workflow by hand: **Actions → release → Run workflow** (or `gh workflow run release.yml`). It builds a signed `0.0.0` APK, verifies it, and attaches it to the run as an artifact.

## App signing: why the key never changes

Android only installs an update if it's signed with the **same certificate** as the installed app. A different key means users must uninstall first, which wipes the pairing. So every release is signed with one permanent key:

| Where | What |
|---|---|
| GitHub secrets | `ANDROID_KEYSTORE_BASE64` (the .jks, base64), `ANDROID_KEYSTORE_PASSWORD`, `ANDROID_KEY_ALIAS`, `ANDROID_KEY_PASSWORD` |
| GitHub variable | `ANDROID_CERT_SHA256` = `13a76c630eb7e2c495fcb1cf5fc0b6faabbc74c919d42c88f1af945ecf88a2dd` |
| Maintainer machine | `~/.config/smsmcp-signing/smsmcp-release.jks` and `keystore.properties` (passwords) |

The release job compares the built APK's certificate with `ANDROID_CERT_SHA256` and **refuses to publish** if they differ. So a swapped or regenerated key can never ship by accident.

Certificate: `CN=smsmcp Gateway, O=salterhoper`, RSA 4096, valid until 2054.

### Back up the key

GitHub secrets can't be read back. If the local copy is lost and the secrets are deleted, no future update can install over existing apps. Keep an offline copy of `~/.config/smsmcp-signing/` (password manager, or an encrypted USB drive).

### Restore the secrets (e.g. after moving the repo)

```sh
D=~/.config/smsmcp-signing; R=salterhoper/smsmcp
PASS=$(grep '^storePassword=' $D/keystore.properties | cut -d= -f2-)
base64 -w0 $D/smsmcp-release.jks | gh secret set ANDROID_KEYSTORE_BASE64 -R $R
printf '%s' "$PASS"  | gh secret set ANDROID_KEYSTORE_PASSWORD -R $R
printf '%s' "$PASS"  | gh secret set ANDROID_KEY_PASSWORD -R $R
printf 'smsmcp'      | gh secret set ANDROID_KEY_ALIAS -R $R
gh variable set ANDROID_CERT_SHA256 -R $R --body 13a76c630eb7e2c495fcb1cf5fc0b6faabbc74c919d42c88f1af945ecf88a2dd
```

### Build a signed APK locally

```sh
cp ~/.config/smsmcp-signing/keystore.properties client/   # git-ignored
cd client && JAVA_HOME=/opt/android-studio/jbr ./gradlew assembleRelease -PversionName=0.1.0 -PversionCode=100
```

Without `keystore.properties` or the `SMSMCP_*` env vars, `assembleRelease` produces an unsigned APK. That's fine for checks but can't be installed.

## Debug vs release builds

Debug builds use the package `com.salterhoper.smsmcp.gateway.debug` and the machine's own debug key. They install **alongside** the release app, never over it. Pair each one separately.

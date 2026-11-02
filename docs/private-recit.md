# Explicit private Recit delivery

TarLink supports one deliberately scoped, opt-in private authority. This is not
a general custom registry feature. It binds GitHub repository ID `1442652140`,
`drobilica/recit`, `catalog/recit/manifest.yaml` on `main`, and app ID `recit`.
The repository must still be private and retain that exact name and numeric ID.
No manifest or lockfile can enroll an authority. Official app-ID collisions are
rejected at enrollment and private installation.

Use a read-only GitHub credential scoped to the Recit repository, supplied in
`TARLINK_PRIVATE_GITHUB_TOKEN`. TarLink never stores it, accepts it in a manifest,
or executes an external credential helper. Enrollment requires the exact numeric
repository ID as an explicit argument:

```sh
tarlink private enroll --repository-id 1442652140
tarlink private install recit@v0.1.0
tarlink private update recit
tarlink verify recit
tarlink rollback recit
tarlink private repair recit
```

The version above is command syntax, not a claim that a given release is retained.
`private install` and `private update` optionally accept an exact retained version;
channel aliases are not part of this narrow interface. `private repair` always
resolves the exact installed identity. Ordinary official-catalog update/repair
cannot change a private installation's authority; use the explicit private route.
Normal local verify, rollback, pin, unpin, run and uninstall work without private
network access. Rollback retains the existing current-plus-one-previous contract.

Private manifests use strict schema v5, one `linux-amd64` platform, `tar.gz`,
SHA-256 and the sole executable `recit` at path `recit`. Runtime, nested archive
and desktop integrations are excluded. Artifact URLs must be canonical immutable
GitHub API asset URLs inside the enrolled repository. TarLink verifies asset
metadata (ID, uploaded state, name and release download path, and GitHub's digest
when present) before acquisition. The exact catalog digest verifies downloaded
bytes before the normal safe extraction and atomic activation lifecycle.

Metadata calls use fixed GitHub API endpoints, 30-second timeouts, a 2 MiB read
bound and no redirects. Binary requests attach credentials only to the selected
API asset URL. Redirects are allowed only to GitHub's exact
`release-assets.githubusercontent.com` HTTPS host, receive no Authorization or
Cookie headers, and retain the downloader's redirect/size bounds. Errors suppress
signed redirect URLs and credential values. A failed online credential check does
not silently fall back to cached authority.

Enrollment lives in a user-owned mode-0600 `private-recit.json` beside the normal
repository-source config. The separately validated private catalog and artifact
cache live under the private `private-recit` cache directory. They never modify
the official registry cache or expose private descriptors through static artifact
repositories. Online operations refresh and validate private metadata; an explicit
`--offline private install` or `--offline private repair` requires a previously
validated private cache plus digest-verified local artifact bytes. Offline install
also requires the previously validated official catalog for collision checking.

Installed state and the resolved package fingerprint include the private authority.
Updates and repairs reject authority changes. Authority does not change on local
rollback; both retained versions belong to the same authority. Existing official
package fingerprints are unchanged. Lock export preserves private authority, but
lock replay rejects private entries with an explicit instruction to use the
private lifecycle; a lock cannot opt a new workstation into a private authority.

Revocation is performed at GitHub or by removing the credential from the calling
environment. It prevents subsequent online metadata requests; it does not revoke
already installed, previously authorized local execution or explicit offline repair.
Checksums and private GitHub hosting do not defend against compromise of the
repository or a malicious process running as the same local user. TarLink does
not execute or sandbox installed applications during acquisition.

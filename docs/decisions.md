# Decisions

Small decided choices that don't need a design doc. Newest first.

## 2026-11-17: each component has its own version, labelled the same way

**Decided (owner, replaces the 2026-11-12 entry below).** `sen`, `sentientd`, `Sentient` (the Mac app) and `SentientES` (the Mac extension) each have their own version; they do not have to match, only be labelled the same way. `sen` and `sentientd` are `major.minor.patch`, set together in the Makefile `VERSION`, `internal/version/version.go` and the tag; they are now `0.1.0`. The app and extension share one Version in the Xcode project (`major.minor`, now `0.1`) plus a Build number that rises on every build, which reads as the patch (`0.1` build 1 is 0.1.1). macOS replaces a system extension only when its version or build is newer (see [mac-collector.md](mac-collector.md)).

**Why.** The Mac pieces ship and change at their own pace, and the extension's build number has to rise on every build anyway.

## 2026-11-12: version numbers are major.minor.build (replaced 2026-11-17)

`sen`, `sentientd` and the Mac app shared one version of the form `major.minor.build`, with the build counting up across minor bumps (the release after `v0.0.3` would be `v0.1.4`, not `v0.1.0`). Replaced by the entry above.

## 2026-11-08: release binaries from GoReleaser on a tag

**Decided.** Pushing a `v*` tag runs `.github/workflows/release.yml`: it runs `go test ./...`, then GoReleaser (`.goreleaser.yaml`) builds `sen` and `sentientd` for linux amd64, linux arm64 and darwin arm64, and publishes `sentient_<version>_<os>_<arch>.tar.gz` and `checksums.txt` as a GitHub release. Each archive holds the two binaries, `sentientd.service`, `README.md` and `LICENSE`. The version is stamped from the tag with the same `-X ...internal/version.Version` flag `make build` uses. The Makefile's `VERSION` and `internal/version/version.go` are bumped by hand before tagging, so a local build and a release agree.

**Why GoReleaser, not a hand-written workflow.** It is the standard tool for Go releases and already does the archives, checksums and release upload. The binaries are pure Go with no CGO, so one Linux runner cross-compiles every target.

**Why tar.gz, not a dmg or pkg.** The two programs are command-line binaries. A dmg is for dragging a `.app` bundle into Applications and would only wrap two files. A signed `.app` bundle matters later, with a native Endpoint Security extension.

**Not signed.** The Mac binaries are not signed or notarized (that needs an Apple Developer ID). Fetched with `curl` they are not quarantined; from a browser, the README says to clear the flag with `xattr`. The Mac build runs in agent logs only mode: there is no macOS collector yet.

**Not built (backlog).** A Homebrew tap (no quarantine, `brew install`), macOS signing and notarization, an Intel Mac build, and an install script.

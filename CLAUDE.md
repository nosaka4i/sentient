# sentient

- **Unit tests, ≥70% coverage.** All code ships with adequate unit tests; keep statement coverage at 70% or above per package (generated protobuf code excluded). Check with `go test -cover ./...`.
- **Mockups** follow the global rule: UI mockups in `.mocks/` (gitignored; pulled to the Mac with `mocks sentient`), CLI mockups in `docs/mocks/cli/<command>.md` (and shown in the session).

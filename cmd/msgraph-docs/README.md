# msgraph-docs

Small OneDrive document CLI using `msauth-go` and `msgraph-go`. It reuses the
established keychain cache, validates the configured account, and never prints
access tokens or signed download URLs. This module keeps auth dependencies out
of library consumer installs.

```sh
go build -o msgraph-docs .
./msgraph-docs --account owner@example.test doctor
./msgraph-docs --account owner@example.test list
./msgraph-docs --account owner@example.test find --query resume
./msgraph-docs --account owner@example.test get --id ITEM_ID
./msgraph-docs --account owner@example.test download --id ITEM_ID --format pdf --out review.pdf
./msgraph-docs --account owner@example.test upload --file review.docx --name new-review.docx
# The preceding upload is an offline plan. This performs the scoped upload:
./msgraph-docs --account owner@example.test upload --file review.docx --name new-review.docx --apply
```

A private declarative profile can replace repeated account flags:

```json
{"account":"owner@example.test"}
```

Pass it with `--profile FILE`. Optional `clientId` and `authority` reuse existing
registrations. Profiles do not contain credentials. Unknown fields, trailing
JSON, and files larger than 16 KiB are rejected.

`Files.Read` permits folder listing, search, downloads, and conversion of an
existing supported document. Uploads request `Files.ReadWrite`; a subscription
is separate from OAuth consent. When an additional grant is actually needed:

```sh
./msgraph-docs --account owner@example.test --write login
```

This opens Microsoft's interactive login. The account must match before staged
cache changes are saved. There is no automatic login or permission escalation.
The CLI requires an explicit account for every network operation. Search may
fail independently of folder listing; `list --id FOLDER_ID` is the fallback.

Uploads are limited to 2 MiB, matching Graph's document-conversion limit. They
create a new file in the account's OneDrive root: preflight plus the server-side
`fail` conflict behavior protect existing files, including a race after preflight.
Downloads stage a complete response before publishing a new local file, never
replace existing output, and reject unsolicited partial content. Mutations are
not automatically retried. If a transport error makes upload success uncertain,
inspect the named destination before retrying; a retry cannot replace it.

Graph stores/converts the document. Editing live Word paragraphs uses the Word
JavaScript API in a connected Office add-in/session. `doctor` deliberately reports
`wordSessionVerified: false`; successful Graph access proves no Word UI session.

Develop with `just docs-ci` at the repository root. Tests use synthetic data and
mocked Graph/auth stores. CI covers this nested module explicitly on the
project-scoped Runmux Linux ARM64 pool. Trusted repository pushes are tested;
public fork pull-request events are denied at the runner policy boundary.
No real sign-in, OneDrive content, or keychain access occurs in CI.

References: [Graph downloads](https://learn.microsoft.com/en-us/graph/api/driveitem-get-content),
[format conversion](https://learn.microsoft.com/en-us/graph/api/driveitem-get-content-format),
[uploads](https://learn.microsoft.com/en-us/graph/api/driveitem-put-content),
[conflict behavior](https://learn.microsoft.com/en-us/graph/api/resources/driveitem),
[Word add-ins](https://learn.microsoft.com/en-us/office/dev/add-ins/word/word-add-ins-programming-overview).

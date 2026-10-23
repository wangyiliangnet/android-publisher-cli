# Third-party notices

The project source is licensed under the MIT License in LICENSE. Dependencies
retain their own copyright notices and licenses.

Go dependencies and their exact versions are recorded in go.mod and go.sum.
The package builder collects their installed license and notice files, together
with the Go standard library license, into THIRD_PARTY_LICENSES.txt in every
binary archive. Packaging fails when a module has no discoverable notice file.

The optional browser observer uses the dependencies pinned in
browser/package-lock.json. Their notices are available in the installed npm
packages. Node.js, Playwright browsers and their third-party components are not
included in the CLI binary archives; consult the notices supplied with those
separately installed runtimes.

Vendor documentation and the apkgo project are protocol research references.
This repository independently implements the protocols and includes no apkgo
source code. The project MIT License does not apply to those external sources.
See docs/channels/protocol-evidence.md for source references and scope.

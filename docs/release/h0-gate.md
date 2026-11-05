# H0 gate: foundation for unattended runs

**Completed:** 2026-11-05
**Scope:** SFT-082 through SFT-085 (H0)
**Release action:** None. No tag, image, or publication.

This is the self-check gate for phase H0. It proves the unattended run loop on
a green tree before any behavior change (SFT-086 onward). Every figure below
was measured in this session; nothing is carried over from earlier tasks.

## Environment

Fedora Linux 44, Linux 7.2.10 x86-64, four logical CPUs, Go 1.27.3,
Node.js 24.21.0, npm 11.19.0. The repository and `/var/tmp` are on btrfs
(`/dev/sda3`); `/tmp` is tmpfs. Final gate runs used the default `TMPDIR`
(`/tmp`, tmpfs); see finding 1.

## Results

Go commands ran with `GOFLAGS=-count=1` so no result came from the test cache.

| Command | Result | Duration |
|---|---|---|
| `make check` (fmt-check, vet, test, build, compose-check) | pass | 29 s |
| `make race-test` | pass | 37 s |
| `make frontend-check` | pass | 2 s |
| `make playwright` | pass, 22 of 22 | 117 s |
| `govulncheck ./...` (v1.8.0, Go 1.27.3) | 0 affected vulnerabilities | 7 s |
| `npm audit --omit=optional` | 0 vulnerabilities | n/a |
| `git diff --check` | clean | n/a |

govulncheck also reports one vulnerability in a required module that the code
does not call: GO-2026-5932 in `golang.org/x/crypto@v0.58.0` (fixed in a
pseudo-version after v0.58.0). It is not reachable, so it does not fail the
gate; the Renovate config from SFT-084 should pick up the fix once a tagged
release exists.

## Findings for the owner

1. **Tests are sensitive to the length of `TMPDIR`.** The control-socket tests
   create `siftail-control.sock` inside `t.TempDir()`, whose name embeds the
   full test name plus a random decimal suffix of 9 or 10 digits. With
   `TMPDIR=/var/tmp` the longest names (for example
   `TestRestoreCLIRequiresStoppedServerConfirmationAndStaysSecretFree`) reach
   108 bytes whenever the suffix has 10 digits, and `bind` fails with
   `invalid argument` (`sun_path` limit), so the test fails about half the
   time. With a longer `TMPDIR` (for example `~/.cache/...`) six more tests
   fail every time: three CLI tests, `TestServeHandlesSIGTERM...`,
   `TestDurableIngestionSubprocessSmoke`, and several `internal/app` tests.
   The default `/tmp` is short enough, so the gate is green there. These are
   test-environment fragilities, not product defects, but a hardening run
   should not depend on them. Suggested follow-up (not done here, out of scope
   for a gate): have those tests put the socket under a short directory (for
   example `os.MkdirTemp("", "sft")`) or shorten the test names.
2. **Real-disk `TMPDIR` for the suite is not viable as-is.** `AGENTS.md` §17.2
   requires a real-disk `TMPDIR` for durability, retention, backup, and
   throughput benchmarks only. Do not set `TMPDIR` globally in `igris.toml`
   for the unattended run; set it per benchmark command with a short path such
   as `/var/tmp`, and only after finding 1 is fixed.
3. **`igris.toml` verify command.** `verify = "make check"` is adequate: it
   finished in 29 s here, far below `verify_timeout = "25m0s"`. The timeout
   could be lowered to about 10 minutes without risk. `make check` does not
   run `race-test`; the gate tasks cover it.
4. **Playwright is the slowest gate (117 s)** and is the only one needing a
   browser; tasks whose Verify cell says `ui` need `make playwright` in their
   own run, as `CLAUDE.md` already states.
5. **No `CLAUDE.md` change is needed.** The rules held; the only friction was
   finding 1, which `CLAUDE.md` could mention if the tests are not fixed.

## Cumulative verification commands

```bash
GOFLAGS=-count=1 make check
GOFLAGS=-count=1 make race-test
make frontend-check
make playwright
govulncheck ./...
npm audit --omit=optional
git diff --check
```

Before the final passing run, `make check` and `make race-test` were run under
a longer `TMPDIR` and under `TMPDIR=/var/tmp` and failed for the reason in
finding 1; no repository file was changed to make them pass.

## What it does

<!-- Tell which connector this PR adds or changes, and what a user can do with it now. -->

## How I tested it

CI has no credentials for a live system. A fork PR gets no secrets. Test against the real system on your own machine, and write the result here. `CONTRIBUTING.md` "Testing" tells how.

- Account type: <!-- for example: personal Microsoft account, GitHub free org, Notion workspace on the free plan -->
- trebi version (`trebi service version`):
- Live commands and the result: <!-- each command that you ran against the real system, and what it did -->

## Limits found in the live run

<!-- API behavior that differs from the docs, and what the skill or the program does about it. Write "none" if you found none. -->

## Checks

- [ ] `scripts/validate.sh <name>` passes.
- [ ] For an entry with events or a channel, `scripts/validate.sh --conformance <name>` passes.
- [ ] `version` is new for a change to an entry that exists.
- [ ] I ran the changed commands against the real system with my own account.
- [ ] Each difference between the real system and the fake system is now in the fake system and has a test.
- [ ] No token, key, password, or personal data is in the PR, in the test data, or in the output above.

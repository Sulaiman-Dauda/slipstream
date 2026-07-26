## What this changes

<!-- What it does and, more importantly, WHY. What failure does it fix? -->

## How you verified it

<!--
Required. "It compiles" is not verification — nearly every bug in this project's history looked
correct in review and was wrong on the wire.

Say what you actually ran, e.g.:
  - `make test` passes
  - `IP=… PANEL_PW=… bash scripts/e2e-verify.sh` → 43/43 on Ubuntu 24.04
  - checked the response headers / listening socket / syscalls on a real box
-->

## Performance impact

<!--
If you touched caching, request handling or tuning defaults, include before/after numbers.
Tuning is A/B measured one directive at a time; anything that does not reproduce a win is not
shipped. Delete this section if it does not apply.
-->

## Checklist

- [ ] `make test` and `go vet` pass
- [ ] Verified on a real server, not just locally
- [ ] Any new assertion tests the **product**, not the test's own setup
- [ ] `docs/` updated if behaviour changed
- [ ] `ui/dist` regenerated and committed if `ui/src` changed

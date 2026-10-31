## Summary

Describe the problem, the change, and why this approach is suitable.

## Related issue

Link the issue or agreed proposal, or explain why an issue or proposal is not
necessary.

## Validation

List the exact commands that you ran, their results, and the relevant tool
versions. Separate offline checks from any authorized integration checks.
List the checks that you did not run and the reasons. Do not mark a planned
check as passed.

```text
Command:
Result:
Not run:
```

## Compatibility and operational impact

Describe the effects on APIs, CRDs, runtime behavior, the portal,
authentication, isolation, storage, configuration, dependencies, or
installations that already exist. If this section does not apply, write `None`.

## Checklist

- [ ] The change is focused and preserves unrelated work.
- [ ] Tests cover the changed behavior, or I explained why no new test is necessary.
- [ ] Documentation and examples match any public behavior changes.
- [ ] Generated artifacts were updated from source, or none are affected.
- [ ] Dependency changes include lockfiles, source attribution, and license information, or none are affected.
- [ ] I examined the diff and attachments for secrets, private endpoints or identifiers, customer data, agent conversations, and internal information.
- [ ] I followed the applicable CLA instructions, or asked a maintainer about an unclear check.
- [ ] I identified checks that must have credentials or a live environment and did not run them without authorization.

<!-- Suspected vulnerabilities belong in the private MSRC reporting process
described in SECURITY.md, not in this pull request. Maintainer review is required. -->

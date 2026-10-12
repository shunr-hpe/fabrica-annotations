<!--
Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC

SPDX-License-Identifier: MIT
-->

# fabrica-annotations

A test bed for the new Fabrica **storage annotations**.

The goal of this project is to exercise, end to end, the annotations that 
control how Fabrica stores a resource — in particular
*dedicated* (hybrid) storage, per-field column promotion, uniqueness, and
composite indexes — and to surface bugs in that code path.

The tests are based on Fabrica's
[12-storage-annotations example](https://github.com/OpenCHAMI/fabrica/tree/main/examples/12-storage-annotations).

The source of truth for the annotations under test is
[user-service-structs/user.go](user-service-structs/user.go). `UserSpec` uses:

- `+fabrica:resource` / `+fabrica:storage=dedicated` — dedicated storage, where
  annotated scalar spec fields become real `spec_*` columns.
- `+fabrica:index:fields=Username,Email:name=idx_user_login:unique:type=btree` —
  a composite unique index.
- Per-field annotations: `immutable`, `unique`, `index`, `sensitive`,
  `storage=hashed:bcrypt`, and `default`.

## Layout

| Path | Purpose |
| --- | --- |
| `user-service-structs/` | The annotated structs under test (`UserSpec`). |
| `fab-init/` | Single-file tool that runs `fabrica add resource` and merges the annotated specs into the generated service. |
| `scripts/generate-user-service` | End-to-end scaffold + generate pipeline. |
| `tests/sqlite/` | Go integration tests that run the generated sqlite service. |
| `build/` | Generated service variants (git-ignored). |

## Common commands

```sh
make            # show help
make generate   # generate sqlite and postgres variants under build/
make test-sqlite
make clean
```

## Observing the uniqueness bug

There is seems to be a bug in the dedicated-storage path: duplicate resources are not
rejected on their unique fields. See **[dev-sqlite.md](dev-sqlite.md)** for
step-by-step directions to generate the sqlite service and reproduce the
problem.

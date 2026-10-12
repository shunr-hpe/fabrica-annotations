<!--
Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC

SPDX-License-Identifier: MIT
-->

# Reproducing the sqlite uniqueness bug

The `UserSpec` struct declares `Username` as unique (and a composite
`idx_user_login` unique index over `Username,Email`). With the dedicated
storage path, that uniqueness is **not enforced**: two users with the same
username can both be created. These directions generate the sqlite service and
show the problem two ways — through the test suite, and by hand.

Prerequisites: `make`, a Go toolchain with CGO (the sqlite build uses
`mattn/go-sqlite3`), plus `sqlite3`, `curl`, and `jq` for the manual walkthrough.

## 1. Via the test suite

Generate the service and run the integration test:

```sh
make clean generate
make test-sqlite
```

`make test-sqlite` runs [tests/sqlite/uniqueness_test.go](tests/sqlite/uniqueness_test.go),
which POSTs the same user twice and asserts the second create fails and the list
holds exactly one user. **The test currently fails** — that failure is the bug.

The test writes its database to `build/user-service-sqlite/user-service-tests.db`
and leaves it in place so you can inspect it:

```sh
cd build/user-service-sqlite
sqlite3 user-service-tests.db ".tables"
sqlite3 user-service-tests.db "SELECT * FROM resources;"

sqlite3 user-service-tests.db ".schema"
```

You should see two rows in `resources` for the same username, confirming the
duplicate was stored.

## 2. By hand

Generate the service and start it:

```sh
make clean generate
```

```sh
cd build/user-service-sqlite
go run ./cmd/server serve --port 8080 --database-url "file:./user-service.db?_fk=1"
```

In another shell, point at the server:

```sh
BASE=http://localhost:8080
```

Confirm the list starts empty:

```sh
curl -s "$BASE/users" | jq
```

Create the user — **run this twice**:

```sh
curl -s -X POST "$BASE/users" \
  -H "Content-Type: application/json" \
  -d '{
    "metadata": { "name": "alice" },
    "spec": {
      "username": "alice",
      "email": "alice@example.com",
      "password": "s3cretpass",
      "fullName": "Alice Example",
      "role": "admin",
      "active": true
    }
  }' | jq
```

The second POST **should** have failed on the unique `username`, but it
succeeds. List the users again and observe two entries with the same username:

```sh
curl -s "$BASE/users" | jq
```


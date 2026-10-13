<!--
Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC

SPDX-License-Identifier: MIT
-->

# Reproducing the postgres uniqueness bug

The `UserSpec` struct declares `Username` as unique (and a composite
`idx_user_login` unique index over `Username,Email`). With the dedicated
storage path, that uniqueness is **not enforced**: two users with the same
username can both be created. These directions generate the postgres service
and show the problem two ways — through the test suite, and by hand.

Prerequisites: `make`, a Go toolchain, Docker (for the postgres container),
plus `curl` and `jq` for the manual walkthrough.

## 1. Via the test suite

Generate the service and run the integration test:

```sh
make clean generate
make test-postgres
```

`make test-postgres` runs [tests/postgres/uniqueness_test.go](tests/postgres/uniqueness_test.go),
which POSTs the same user twice and asserts the second create fails and only one
such user is stored. **The test currently fails** — that failure is the bug.
With no `USER_SERVICE_POSTGRES_URL` set, the test starts its own `postgres:16`
container (port 5432 if free, otherwise a free port) and removes it when done.

### Stepped variant

To keep postgres up across several runs (and do your own poking around between
them), drive the steps yourself:

```sh
make postgres-up                # start postgres on 5432
make test-postgres-only-tests   # run the tests against it (no Docker management)
make postgres-down              # stop postgres
```

## 2. By hand

Start postgres directly (not via a make target) and record its URL:

```sh
export PG="postgres://postgres:postgres@localhost:5432/user_service?sslmode=disable"
docker run --rm -d --name user-pg -p 5432:5432 \
  -e POSTGRES_USER=postgres \
  -e POSTGRES_PASSWORD=postgres \
  -e POSTGRES_DB=user_service \
  postgres:16
```

Generate the service and start it against that database:

```sh
make clean generate
```

```sh
cd build/user-service-postgres
go run ./cmd/server serve --port 8080 --database-url "$PG"
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

Inspect the database directly with `psql` inside the container:

```sh
docker exec user-pg psql -U postgres -d user_service -c "\dt"
docker exec user-pg psql -U postgres -d user_service -c "SELECT * FROM resources;"

docker exec user-pg psql -U postgres -d user_service -c "\d resources"
```

You should see two rows in `resources` for the same username, confirming the
duplicate was stored.

Tear down postgres when finished. The `-v` removes the anonymous data volume
along with the container:

```sh
docker rm -f -v user-pg
```

If earlier runs left orphaned anonymous volumes behind, clear them with:

```sh
docker volume prune
```

## The fix

This is a known Fabrica bug in the dedicated-resources storage path. The fix
lives on the `shunr-hpe/dedicated-resources` branch; nothing in this project
works around it.

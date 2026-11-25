# Migrations

- Never edit a migration that has been applied (anything already on `main`). `scripts/migrate.sh` records applied files by name in `schema_migrations` and skips them, so an edit silently never runs on an existing database. CI's `e2e` starts from an empty database and won't catch it. Add a new file with the next number instead.
- One migration runs as one transaction (`migrate.sh` wraps it in BEGIN/COMMIT), so don't put statements in it that Postgres can't run inside a transaction, such as `CREATE INDEX CONCURRENTLY`.

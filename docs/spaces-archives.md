# Cold task archives in DigitalOcean Spaces

The task archive worker supports `local` (default) and S3-compatible object storage.
Running task workspaces, PostgreSQL, and other active data remain local. This is
not a live filesystem mount or a general database backup.

## Configuration

Use a private Standard Spaces bucket in SFO3, with CDN and public listing disabled.
Create a dedicated access key with Read/Write/Delete permission limited to this
bucket. Do not grant full account access. Store these settings in a root-owned
0600 systemd EnvironmentFile, never in source control or the frontend:

```dotenv
ARTEX_ARCHIVE_STORAGE=s3
ARTEX_ARCHIVE_S3_ENDPOINT=https://sfo3.digitaloceanspaces.com
ARTEX_ARCHIVE_S3_REGION=us-east-1
ARTEX_ARCHIVE_S3_BUCKET=your-artex-archives
ARTEX_ARCHIVE_S3_PREFIX=artex/tasks
ARTEX_ARCHIVE_S3_ACCESS_KEY_ID=REPLACE
ARTEX_ARCHIVE_S3_SECRET_ACCESS_KEY=REPLACE
```

The endpoint selects the storage datacenter; the region is the S3 signing region.
Optional `ARTEX_ARCHIVE_S3_PATH_STYLE=true` supports compatible path-style servers.
Configured but invalid S3 settings fail new archive jobs without falling back to
local storage. Keep the bucket/prefix and credentials available while remote
archives exist. Do not use the upstream one-click updater: it replaces this custom
build; future upgrades must carry this patch forward.

## Behavior and integrity

1. Pause or finish a task, then use its existing Archive action.
2. ARTEX creates its existing `.tar.zst` package locally, uploads it (multipart for
   larger files), then reads the entire object back and verifies size and SHA-256.
3. Only after successful verification does PostgreSQL commit the `s3://` location
   and compact the task. Local source files and the temporary package are cleaned.
4. Restore downloads and verifies the package, restores files and PostgreSQL, then
   consumes the remote package. Failure to delete is retryable without replaying
   restored records. Delete also uses a durable cleanup journal to cover crashes.

The UI identifies local versus object storage per archive. Existing local
archives remain usable. No schema migration or archive format change is required.
Manual tar archives outside ARTEX's task archive database are not automatically
imported, uploaded, or deleted.

Archive creation still needs room for the compressed package; restoration needs
room for the downloaded package plus extracted files. Full readback verification
and later restores consume Spaces outbound transfer allowance. Interrupted
multipart uploads may leave parts after an abrupt kill; consider an incomplete
multipart expiration rule in Spaces. Keep bucket objects private.

## Validation and recovery

`go run ./cmd/artex-archive-check` uses the configured environment to put, verify,
download and delete a small uniquely named test object. It never operates on task
data. Run it with the same environment as the service.

Recovery journals under `data/archives/tasks/.remote` contain no access keys.
Preserve these alongside PostgreSQL and the rest of the data directory. The worker
retries object cleanup after startup and before subsequent jobs. Do not manually
remove bucket objects or these journals while jobs are running.

Tests use an HTTP S3 fixture and an isolated PostgreSQL instance, covering
multipart uploads, corrupted data, upload rollback, local compatibility,
restoration with failed object cleanup, and the persistent deletion outbox.

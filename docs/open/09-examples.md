# 09 · Learning examples

`examples/` holds the code written while first learning Go concurrency, using a simplified
movie/seat domain. It is kept as a reference only: it is not part of the application, is not
imported by it, and its tests are not part of the regular test runs.

## Contents

| Path | What it shows |
|---|---|
| `examples/booking/memory_store.go` | `MemoryStore`: a plain map, **not** safe for concurrent use; the original concurrent test fails or crashes against it. |
| `examples/booking/concurrent_store.go` | `ConcurrentStore`: the same map guarded by a `sync.RWMutex`. |
| `examples/booking/redis_store.go` | `RedisStore`: seat holds with `SET NX` and a TTL, plus a session → seat reverse lookup. |
| `examples/adapters/redis/redis.go` | A Redis client constructor. |
| `examples/booking/service_test.go` | 10,000 goroutines try to book one seat; exactly one must win. Needs Redis from `docker compose up -d`. |

## Lessons the real implementation must not repeat

- `session:{id}` stores only one seat key, so a second hold in the same session overwrites it and
  orphans the first seat until its TTL expires.
- The cleanup `DEL` is not token-safe: it can delete a hold that belongs to someone else.
  Releases must be compare-and-delete (invariant I11).
- Redis is the only correctness mechanism. In the real design Postgres decides and Redis is only a
  pre-filter (invariant I10).
- `ListBookings` panics, and there is no confirm or release path.
- `context.Background()` is created inside methods; `ctx` should be the first parameter.
- `log.Fatalf` in library code kills the process; it should return an error.
- `defaultHoldTTL` was defined in two places with different values. Each configuration value must
  have one source.
- `Status` is a free-form string; it should be a typed state with explicit transitions
  (invariant I2).

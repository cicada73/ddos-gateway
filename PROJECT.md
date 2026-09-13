# Real-Time Abuse/DDoS Mitigation Gateway

**A reverse-proxy gateway that detects and blocks abusive traffic in real time — distributed correctly across replicas, tested against a live simulated attack it generates itself, and observable end-to-end on a live dashboard.**

---

## 1. What this project is, and why it's built this way

Most system-design portfolio projects (URL shorteners, basic rate limiters, chat apps) are extremely common and don't touch security specifically. This project deliberately sits at the intersection of **distributed systems fundamentals** (shared state, horizontal scaling, load balancing, observability) and a **cybersecurity angle** (abuse detection, adversarial traffic simulation, evasion techniques). Pairing the two — rather than doing either alone — is the differentiator.

**The elevator pitch:** *"A gateway that survives a live simulated attack, catches evasion techniques a naive rate limiter would miss, and can prove all of it on a real-time dashboard."*

**The five phases, in one line each:**
1. Single-instance gateway with in-memory rate limiting (the core mechanism)
2. Made it distributed — Redis-backed shared state across replicas, behind nginx (the mechanism survives scaling)
3. Built an attack simulator (Locust) and proved the mitigation works with real numbers
4. Added anomaly detection to catch evasion the rate limiter alone can't see
5. Made all of it observable in real time with Prometheus + Grafana

---

## 2. Full architecture (final state, through Phase 5)

```
                          ┌──────────────┐
      client requests →  │    nginx     │   load balancer, :8080
                          └──────┬───────┘
                    round-robin  │
                ┌─────────────────┴─────────────────┐
                ▼                                   ▼
         ┌─────────────┐                     ┌─────────────┐
         │  gateway-a   │                     │  gateway-b   │   Go reverse proxies
         └──────┬───────┘                     └──────┬───────┘
                │ 1. anomaly check (key/IP cycling)   │
                │ 2. token bucket check               │
                └─────────────────┬───────────────────┘
                                  │
            ┌─────────────────────┼─────────────────────┐
            ▼                     ▼                      ▼
     ┌─────────────┐       ┌─────────────┐        ┌─────────────┐
     │    Redis     │       │   backend    │        │  Prometheus  │
     │ (shared rate- │       │  (demo API)  │        │ (scrapes     │
     │ limit + anomaly│      │              │        │ /metrics     │
     │  state)       │       │              │        │ every 5s)    │
     └─────────────┘       └─────────────┘        └──────┬──────┘
                                                           ▼
                                                    ┌─────────────┐
                                                    │   Grafana    │
                                                    │ (live         │
                                                    │  dashboard)   │
                                                    └─────────────┘
```

Every request enters through nginx, gets load-balanced to one of two gateway replicas, then passes two independent checks — an anomaly check (is this identity's *behavior pattern* suspicious?) and a token bucket check (is this identity individually over its rate?) — both backed by state shared in Redis so the checks are correct regardless of which replica handles which request. Allowed requests reach the backend; blocked ones never do. Every outcome is recorded as a Prometheus metric and visualized live in Grafana.

---

## 3. Phase-by-phase breakdown

### Phase 1 — Single-instance gateway, in-memory rate limiting

**Goal:** the smallest version of the system that's still "real" — a genuine production-grade reverse proxy (Go's `httputil.NewSingleHostReverseProxy`), correctly deciding allow/block per request.

**Built:**
- `backend/main.go` — minimal demo API (`GET/POST /items`, `/health`), deliberately knows nothing about the gateway.
- `gateway/limiter/tokenbucket.go` — token bucket algorithm, isolated in its own package (`Allow(key) (bool, float64)`). Each key gets a bucket of 20 tokens, refilling continuously at 5/sec — continuous refill (not a hard reset) is what lets it absorb bursts while capping the long-run rate.
- `gateway/main.go` — the proxy + a middleware wrapping it: extract identity (`X-API-Key` header, else source IP) → check bucket → forward or return `429`.

**Proven:** burst of 25 requests → ~20 succeed then `429`; independent buckets per key; partial refill after waiting; POST bodies proxy correctly.

**The gap left open:** everything lives in one process's memory. Two replicas would each keep their own map — a client bounced between them gets double the intended limit, since neither instance knows what the other counted.

---

### Phase 2 — Making it distributed (Redis + nginx)

**Goal:** close that exact gap — the rate limit must hold correctly no matter how traffic is spread across replicas.

**Built:**
- `gateway/limiter/tokenbucket.lua` — the same check-and-consume logic, but as a single **atomic** script run inside Redis. Without this, two replicas could both read "3 tokens left," both allow, both write "2 tokens left" — silently letting one extra request through. The script also uses Redis's own clock (`TIME`) instead of each replica's local clock, so machine clock drift can't corrupt the refill math.
- `gateway/limiter/redis_tokenbucket.go` — thin Go wrapper exposing the same `Allow()` shape, so the middleware didn't need to change.
- `gateway/main.go` (updated) — connects to Redis; introduces a **fail-open policy**: if Redis is unreachable, log it and let the request through rather than blocking all traffic. (The alternative, fail-closed, takes the whole API down on a Redis hiccup — a real trade-off, not an oversight.)
- `docker-compose.yml`, `infra/nginx.conf`, two `Dockerfile`s — Redis, backend, **two** gateway replicas, nginx round-robin, all via one `docker compose up --build`.

**Proven:**
- Quantified the exact bug: two independent in-memory limiters (Phase 1 naively scaled) allowed **40 requests instead of the intended 20** — exactly double.
- Proved the fix: two real gateways + real Redis, alternating requests under one identity → **exactly 20 allowed, then clean `429`s**, regardless of which replica handled which request.
- Verified the full containerized stack end-to-end (all 5 containers), nginx's own access log confirming correct load-balancing.

**Real bugs hit and fixed along the way:** a Go toolchain version mismatch between the local machine and the Docker base image (fixed by pinning `go.mod`); a stale Phase 1 process still holding port 8080; a malformed one-line `.gitignore` that wasn't actually excluding anything.

---

### Phase 3 — Attack simulator (Locust) and the before/after proof

**Goal:** generate genuine concurrent load — not one-curl-process-at-a-time — and produce real evidence the gateway works.

**Built:** `simulator/locustfile.py` with two concurrent user classes:
- `NormalUser` — realistic pacing (0.5-2s between requests), stable identity. Traffic that should never be impeded.
- `AttackUser` — zero wait time, drawn from a small fixed pool of 5 identities — mimicking a real credential-stuffing/burst attack (reusing a handful of identities and hammering them), not spreading across thousands of unique low-rate ones.

`docker-compose.yml` updated to expose the backend directly on host port 9000 too, purely so the identical traffic profile could be fired at it with no gateway in front — an unmitigated baseline for comparison.

**Proven (verified on-machine, 15s test, 30 simulated users):**

| | Unmitigated (direct to backend) | Mitigated (through gateway) |
|---|---|---|
| Attack requests attempted | 9,198 | 9,081 |
| Attack requests allowed through | 9,198 (100%) | 367 (4.0%) |
| Attack requests blocked | 0 | 8,714 (96%) |
| Normal traffic failures | 0 | 0 |

The critical result isn't just that attack traffic gets blocked — **normal traffic saw zero failures in both scenarios.** The gateway suppresses specifically the identities hammering it, leaving well-behaved clients untouched.

---

### Phase 4 — Anomaly detection: catching what the rate limiter can't see

**Goal:** the token bucket asks "is THIS identity sending too many requests?" — but an attacker can evade that entirely by spreading requests across many different identities, none individually excessive. Phase 4 asks a different question: "does this identity's *behavior pattern* look like an attack?"

**The evasion it closes:** rotating through 25 different API keys at 3-4 requests each would sail straight through Phases 1-3's rate limiter — no single key ever nears its limit. That's a real technique (key cycling / credential stuffing).

**Built:**
- `gateway/limiter/anomaly.go` — `IdentityCycleDetector`: tracks, per primary identity, the set of distinct secondary identities recently seen (a Redis set with a rolling TTL), and flags the primary once that set exceeds a threshold. A flag is a separate Redis key with its own cooldown TTL — the identity is blocked outright for that whole period, not just until the tracking window resets.
- Two instances wired into `gateway/main.go`: an **IP** using >5 distinct **API keys** within 60s → flagged (key cycling); an **API key** seen from >5 distinct **IPs** within 60s → flagged (shared/stolen key). These checks run *before* the token bucket, returning `403` (distinct from the token bucket's `429`) so the status code alone tells you which mechanism caught a request.
- `extractIdentity()` replaced `clientKey()` — returns IP and API key separately, since anomaly detection is about the *relationship* between the two.

**Proven:** 10 requests, 1 second apart, each with a **different** API key (each key sent exactly one request in its life — nowhere near any bucket limit). Requests 1-5: `200`. Requests 6-10: `403` — the 6th distinct key tripped the flag, and the IP stayed blocked for the cooldown. None of this ever touched the rate limiter; it was caught purely on the *pattern*.

---

### Phase 5 — Observability: Prometheus + Grafana

**Goal:** make everything the gateway is deciding visible in real time, instead of only inferable from logs after the fact.

**Built:**
- `gateway/metrics.go` — three Prometheus metrics, each answering one concrete question:
  - `gateway_requests_total{outcome, replica}` — how many requests, and what happened to them? (`outcome`: `allowed` / `rate_limited` / `anomaly_blocked`)
  - `gateway_request_duration_seconds{outcome}` — how fast is the gateway making that decision?
  - `gateway_anomaly_new_flags_total{reason}` — how many *new* anomalous identities have been caught (not every request blocked by an existing flag — just the moment a new one is raised)?
- `gateway/main.go` (updated) — every request's outcome and duration are recorded via a `defer`, and a `/metrics` endpoint is exposed on its own route (never passing through the rate-limit/anomaly middleware itself).
- `infra/prometheus.yml` — scrapes both `gateway-a:8080/metrics` and `gateway-b:8080/metrics` every 5 seconds.
- `infra/grafana/` — datasource and dashboard fully **provisioned automatically** (no manual Grafana setup): a "DDoS Mitigation Gateway" dashboard with six panels — request rate by outcome, total requests (5m), blocked % (5m), request rate by replica (ties back to Phase 2's load balancing), new anomaly flags (5m), and p95 latency by outcome.
- `docker-compose.yml` (updated) — added `prometheus` (port 9090) and `grafana` (port 3000, default login `admin`/`admin`) services.

**Proven:** generated a mix of normal, burst, and key-cycling traffic and watched the dashboard update live within its 5-second refresh — all three outcome types appeared as distinct series, the replica-split panel showed traffic dividing across `gateway-a` and `gateway-b`, and the anomaly-flags panel lit up exactly when the cycling traffic tripped a flag.

---

## 4. Repository structure (final)

```
ddos-gateway/
├── PROJECT.md                        ← this file
├── docker-compose.yml
├── backend/
│   ├── go.mod
│   ├── main.go
│   └── Dockerfile
├── gateway/
│   ├── go.mod
│   ├── go.sum
│   ├── main.go
│   ├── metrics.go
│   ├── Dockerfile
│   └── limiter/
│       ├── tokenbucket.go            (Phase 1 — in-memory, kept for reference)
│       ├── redis_tokenbucket.go      (Phase 2 — Redis-backed, currently used)
│       ├── tokenbucket.lua           (atomic script run inside Redis)
│       └── anomaly.go                (Phase 4 — key/IP cycling detection)
├── simulator/
│   ├── locustfile.py
│   └── requirements.txt
├── infra/
│   ├── nginx.conf
│   ├── prometheus.yml
│   └── grafana/
│       ├── provisioning/
│       │   ├── datasources/datasource.yml
│       │   └── dashboards/dashboard.yml    (provider config)
│       └── dashboards/
│           └── gateway-dashboard.json      (the actual dashboard)
└── docs/
    ├── PROJECT_EXPLAINER.md          (earlier phase-by-phase build log)
    └── sample_reports/               (Locust HTML/CSV reports from Phase 3)
```

---

## 5. Live demo script (start to finish)

A complete walkthrough for presenting this project live to someone else — each step has an exact command and what to point out while it runs.

### Setup (before anyone's watching)
1. Make sure Docker Desktop is running.
2. From the project root, run:
   ```
   docker compose up --build
   ```
   Wait for all **7** containers (`redis`, `backend`, `gateway-a`, `gateway-b`, `nginx`, `prometheus`, `grafana`) to log their "listening"/"ready" messages. Leave this terminal open and visible — the logs are good background evidence during the demo.
3. Open a second terminal for running commands.
4. Open `http://localhost:3000` in a browser tab (Grafana, login `admin`/`admin`) and leave it visible throughout — this is where most of the "wow" happens live.

### Step 1 — Show the basic system working
```
curl.exe http://localhost:8080/items
```
**Say:** "This is a request going through nginx, load-balanced to one of two gateway replicas, which checks it against Redis-backed rate limiting before proxying it to the backend." Point at the Docker Compose log window — you'll see the request land on either `gateway-a` or `gateway-b`.

### Step 2 — Demonstrate rate limiting (Phase 1-2)
```powershell
1..25 | ForEach-Object { curl.exe -s -o NUL -w "req $_ -> %{http_code}`n" -H "X-API-Key: demo-user" http://localhost:8080/items }
```
**Say:** "20 requests succeed, then it starts returning 429 — that's a token bucket, shared correctly across both replicas via Redis, not just one instance's memory." Switch to the Grafana tab — the "Request rate by outcome" panel should show a spike including `rate_limited`.

### Step 3 — Demonstrate anomaly detection (Phase 4) — the key differentiator
```powershell
1..10 | ForEach-Object { curl.exe -s -o NUL -w "req $_ -> %{http_code}`n" -H "X-API-Key: rotating-key-$_" http://localhost:8080/items; Start-Sleep -Milliseconds 500 }
```
**Say:** "Every one of these keys only ever sends ONE request — nowhere near the rate limit. But watch: after the 5th distinct key, it starts returning 403, not 429. That's a completely separate detection layer catching *credential stuffing / key-cycling* — an evasion technique a plain rate limiter would never notice." Point at Grafana's "New anomaly flags" panel ticking up.

### Step 4 — Show it live on the dashboard
Walk through the Grafana panels directly:
- **Request rate by outcome** — the three colors/series from steps 1-3.
- **Request rate by replica** — traffic split across `gateway-a` / `gateway-b`, tying back to the distributed design.
- **Blocked % (5m)** and **Total requests (5m)** — the headline numbers at a glance.
- **p95 latency by outcome** — the gateway's decision overhead is milliseconds, even while blocking.

### Step 5 — The grand finale: attack simulator before/after (Phase 3)
```powershell
cd simulator
python -m locust -f locustfile.py --host=http://localhost:9000 --headless -u 30 -r 10 -t 15s --csv unmitigated --html unmitigated.html
python -m locust -f locustfile.py --host=http://localhost:8080 --headless -u 30 -r 10 -t 15s --csv mitigated --html mitigated.html
```
**Say:** "Same simulated attack traffic, fired twice — once straight at the backend with no protection, once through the gateway. Watch the failure rate." Show the two summary tables side by side (or open the generated `.html` reports): **100% of attack traffic gets through unmitigated vs. ~96% blocked when the gateway is in front — with zero impact on legitimate traffic in either case.**

### Closing talking points
1. **The core problem:** naive rate limiting breaks the moment you scale horizontally — this project demonstrates why, quantifies it (40 vs. 20 requests), and fixes it with atomic, clock-consistent shared state.
2. **Why Lua-in-Redis, not "call Redis twice":** atomicity — a read-then-write across two network calls has a race window; a Lua script doesn't.
3. **The fail-open decision:** a real, named trade-off (availability vs. strict enforcement), not an accidental gap.
4. **The anomaly layer is the differentiator:** it's what separates this from "yet another rate limiter" — it specifically targets an evasion technique, tying the project to a security specialization rather than being generic distributed-systems practice.
5. **It's observable, not just functional:** the Grafana dashboard means every claim above can be shown live, not just asserted.

---

## 6. Quick reference: all commands in one place

```powershell
# Bring up everything
docker compose up --build

# Basic sanity check
curl.exe http://localhost:8080/items

# Rate limit burst test
1..25 | ForEach-Object { curl.exe -s -o NUL -w "req $_ -> %{http_code}`n" -H "X-API-Key: test" http://localhost:8080/items }

# Anomaly detection (key cycling) test
1..10 | ForEach-Object { curl.exe -s -o NUL -w "req $_ -> %{http_code}`n" -H "X-API-Key: rotating-key-$_" http://localhost:8080/items; Start-Sleep -Milliseconds 500 }

# Attack simulator (from simulator/ folder, after `python -m pip install -r requirements.txt`)
python -m locust -f locustfile.py --host=http://localhost:9000 --headless -u 30 -r 10 -t 15s --csv unmitigated --html unmitigated.html
python -m locust -f locustfile.py --host=http://localhost:8080 --headless -u 30 -r 10 -t 15s --csv mitigated --html mitigated.html

# Grafana dashboard
# http://localhost:3000  (admin / admin)

# Prometheus raw metrics (for debugging queries)
# http://localhost:9090
```

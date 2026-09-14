"""
Browser-visual demo simulator. Separate from locustfile.py (the Phase 3
simulator, which targets /items and whose results are already recorded in
PROJECT.md) so that file's numbers stay exactly reproducible.

This one targets "/" - the dummy storefront homepage - specifically so you
can watch it in an actual browser tab while the attack runs, rather than
only reading numbers in a terminal.

Usage: open a browser tab to the target BEFORE starting this, then run it
against the unprotected backend and the gateway separately, same as
Phase 3's before/after:

  Unprotected (open http://localhost:9000/ in a browser tab first):
    python -m locust -f locustfile_homepage.py --host=http://localhost:9000 --headless -u 40 -r 10 -t 30s

  Protected (open http://localhost:8080/ in a browser tab first):
    python -m locust -f locustfile_homepage.py --host=http://localhost:8080 --headless -u 40 -r 10 -t 30s

Reload the browser tab a few times while each test runs. Against :9000 the
page should visibly slow down or hang (the backend's simulated capacity -
8 concurrent requests - gets saturated by attack traffic). Against :8080
it should stay responsive, since the gateway stops most of the attack
traffic before it ever reaches the backend.
"""

import random

from locust import HttpUser, task, between, constant


class NormalUser(HttpUser):
    weight = 1  # fewer distinct normal identities - keeps this demo clear
    # of the documented single-test-machine edge case (many identities
    # genuinely sharing one real IP when load-testing from one machine).
    # Phase 3's original locustfile.py already covers a realistic mixed
    # traffic ratio - this file's job is specifically to make the
    # visual capacity effect obvious, which needs attacker CONNECTION
    # volume, not a large distinct normal-user population.
    wait_time = between(1, 3)

    def on_start(self):
        self.api_key = f"visitor-{random.randint(1, 1000)}"

    @task
    def browse_homepage(self):
        self.client.get("/", headers={"X-API-Key": self.api_key}, name="/ [normal browsing]")


class AttackUser(HttpUser):
    weight = 5  # many concurrent connections, reusing a SMALL identity
    # pool. This is deliberate: the rate limiter correctly caps what
    # those 5 identities are ALLOWED to send regardless of how many
    # connections pile on behind them - more attacker connections
    # increases raw unmitigated pressure without increasing allowed
    # throughput once mitigated, which is exactly the point being shown.
    wait_time = constant(0)

    def on_start(self):
        # Same small fixed pool as Phase 3 - a real attack reuses a handful
        # of identities and hammers them, rather than spreading across many.
        self.api_key = f"attacker-{random.randint(1, 5)}"

    @task
    def hammer_homepage(self):
        self.client.get("/", headers={"X-API-Key": self.api_key}, name="/ [ATTACK]")
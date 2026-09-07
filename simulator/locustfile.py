"""
Attack simulator for the DDoS mitigation gateway.

Two user profiles run concurrently in every test:

  NormalUser  - a realistic client: a handful of requests, sensible pacing
                (0.5-2s between requests), each with its own stable identity.
                This is the traffic the gateway should never impede.

  AttackUser  - a small pool of identities hammering as fast as physically
                possible (no wait time between requests), simulating a
                credential-stuffing / burst-flood attacker. This is the
                traffic the gateway should suppress.

Why a small, fixed pool of attacker identities (not thousands of random
ones): a real volumetric/credential-stuffing attack typically reuses a
limited set of credentials or IPs, hammering them hard - it does NOT look
like thousands of unique low-rate users (that would just be normal-looking
traffic at scale). Keying the limiter by identity, as the gateway does,
specifically targets this pattern.

Run the SAME test twice to see the mitigation's effect in isolation:

  Against the raw backend (no gateway in front, mitigation OFF):
    locust -f locustfile.py --host=http://localhost:9000 --headless \
      -u 30 -r 10 -t 30s --html report_unmitigated.html --csv unmitigated

  Against the gateway/nginx (mitigation ON):
    locust -f locustfile.py --host=http://localhost:8080 --headless \
      -u 30 -r 10 -t 30s --html report_mitigated.html --csv mitigated

Same traffic, same duration, only the target differs - so any difference
in throughput/latency/failure rate is attributable to the gateway itself.
"""

import random

from locust import HttpUser, task, between, constant


class NormalUser(HttpUser):
    weight = 3  # majority of simulated users are legitimate
    wait_time = between(0.5, 2)

    def on_start(self):
        self.api_key = f"user-{random.randint(1, 1000)}"

    @task(3)
    def list_items(self):
        self.client.get("/items", headers={"X-API-Key": self.api_key}, name="/items [normal GET]")

    @task(1)
    def create_item(self):
        self.client.post(
            "/items",
            json={"name": "widget"},
            headers={"X-API-Key": self.api_key},
            name="/items [normal POST]",
        )


class AttackUser(HttpUser):
    weight = 1  # fewer simulated attackers than normal users...
    wait_time = constant(0)  # ...but each one fires with no pacing at all

    def on_start(self):
        # Small fixed pool: attackers reuse a handful of identities and
        # hammer them, rather than spreading across many unique ones.
        self.api_key = f"attacker-{random.randint(1, 5)}"

    @task
    def hammer(self):
        self.client.get("/items", headers={"X-API-Key": self.api_key}, name="/items [ATTACK]")
"""Concurrent operation tests — parallel reads, writes, searches."""

import time
from concurrent.futures import ThreadPoolExecutor, as_completed

import httpx
import pytest

from conftest import VECDEX_BASE_URL, VECDEX_API_KEY, unique_name


pytestmark = pytest.mark.p2


def make_client() -> httpx.Client:
    """Create a fresh client for thread-safe usage."""
    return httpx.Client(
        base_url=VECDEX_BASE_URL,
        headers={"Authorization": f"Bearer {VECDEX_API_KEY}"},
        timeout=30.0,
    )


class TestConcurrentWrites:
    """Parallel PUT operations."""

    def test_concurrent_upsert_different_docs(self, client, collection_factory):
        """Multiple threads upserting different documents simultaneously."""
        coll = collection_factory()
        name = coll["name"]
        n = 10

        def upsert(i: int) -> int:
            c = make_client()
            try:
                resp = c.put(
                    f"/collections/{name}/documents/par-{i}",
                    json={"content": f"parallel doc number {i}"},
                )
                return resp.status_code
            finally:
                c.close()

        with ThreadPoolExecutor(max_workers=5) as pool:
            futures = [pool.submit(upsert, i) for i in range(n)]
            statuses = [f.result() for f in as_completed(futures)]

        assert all(s in (200, 201) for s in statuses)

        # Verify all docs exist
        for i in range(n):
            resp = client.get(f"/collections/{name}/documents/par-{i}")
            assert resp.status_code == 200

    def test_concurrent_upsert_same_doc(self, client, collection_factory):
        """Multiple threads upserting the same document — last writer wins."""
        coll = collection_factory()
        name = coll["name"]

        def upsert(i: int) -> int:
            c = make_client()
            try:
                resp = c.put(
                    f"/collections/{name}/documents/race-1",
                    json={"content": f"version {i}"},
                )
                return resp.status_code
            finally:
                c.close()

        with ThreadPoolExecutor(max_workers=5) as pool:
            futures = [pool.submit(upsert, i) for i in range(10)]
            statuses = [f.result() for f in as_completed(futures)]

        assert all(s in (200, 201) for s in statuses)

        # Doc should exist with one of the versions
        resp = client.get(f"/collections/{name}/documents/race-1")
        assert resp.status_code == 200
        assert "version" in resp.json()["content"]


class TestConcurrentSearch:
    """Parallel search operations."""

    def test_concurrent_search(self, client, populated_collection):
        """Multiple threads searching simultaneously."""
        coll = populated_collection["name"]

        def search(query: str) -> int:
            c = make_client()
            try:
                resp = c.post(
                    f"/collections/{coll}/documents/search",
                    json={"query": query, "mode": "semantic"},
                )
                return resp.status_code
            finally:
                c.close()

        queries = [
            "programming language",
            "container orchestration",
            "database caching",
            "static typing",
            "web development",
        ]

        with ThreadPoolExecutor(max_workers=5) as pool:
            futures = [pool.submit(search, q) for q in queries]
            statuses = [f.result() for f in as_completed(futures)]

        assert all(s == 200 for s in statuses)


class TestConcurrentMixed:
    """Mixed read/write/search operations."""

    def test_read_write_search_parallel(self, client, collection_factory):
        """Concurrent GET, PUT, and search on the same collection."""
        coll = collection_factory()
        name = coll["name"]

        # Seed one doc
        client.put(
            f"/collections/{name}/documents/seed",
            json={"content": "initial seed document for concurrent test"},
        )
        time.sleep(0.3)

        errors = []

        def writer(i: int):
            c = make_client()
            try:
                resp = c.put(
                    f"/collections/{name}/documents/w-{i}",
                    json={"content": f"written by thread {i}"},
                )
                if resp.status_code not in (200, 201):
                    errors.append(f"write w-{i}: {resp.status_code}")
            finally:
                c.close()

        def reader():
            c = make_client()
            try:
                resp = c.get(f"/collections/{name}/documents/seed")
                if resp.status_code != 200:
                    errors.append(f"read seed: {resp.status_code}")
            finally:
                c.close()

        def searcher():
            c = make_client()
            try:
                resp = c.post(
                    f"/collections/{name}/documents/search",
                    json={"query": "seed document", "mode": "semantic"},
                )
                if resp.status_code != 200:
                    errors.append(f"search: {resp.status_code}")
            finally:
                c.close()

        with ThreadPoolExecutor(max_workers=8) as pool:
            futs = []
            for i in range(5):
                futs.append(pool.submit(writer, i))
            for _ in range(3):
                futs.append(pool.submit(reader))
            for _ in range(3):
                futs.append(pool.submit(searcher))
            for f in as_completed(futs):
                f.result()

        assert len(errors) == 0, f"Concurrent errors: {errors}"


class TestConcurrentPatch:
    """Parallel PATCH operations."""

    def test_parallel_patch_different_fields(self, client, collection_factory):
        """13.2: Parallel PATCH on different fields of same doc."""
        coll = collection_factory(
            fields=[
                {"name": "a", "type": "tag"},
                {"name": "b", "type": "tag"},
            ]
        )
        name = coll["name"]

        client.put(
            f"/collections/{name}/documents/patch-race",
            json={"content": "concurrent patch test", "tags": {"a": "1", "b": "2"}},
        )

        def patch_a():
            c = make_client()
            try:
                return c.patch(
                    f"/collections/{name}/documents/patch-race",
                    json={"tags": {"a": "updated-a"}},
                ).status_code
            finally:
                c.close()

        def patch_b():
            c = make_client()
            try:
                return c.patch(
                    f"/collections/{name}/documents/patch-race",
                    json={"tags": {"b": "updated-b"}},
                ).status_code
            finally:
                c.close()

        with ThreadPoolExecutor(max_workers=2) as pool:
            f1 = pool.submit(patch_a)
            f2 = pool.submit(patch_b)
            assert f1.result() == 200
            assert f2.result() == 200

        # Doc should exist and have the content intact
        doc = client.get(f"/collections/{name}/documents/patch-race").json()
        assert doc["content"] == "concurrent patch test"


class TestConcurrentSearchHighLoad:
    """High-load parallel search operations."""

    def test_100_parallel_searches(self, client, populated_collection):
        """13.5: 100 parallel search requests."""
        coll = populated_collection["name"]

        def search(i: int) -> int:
            c = make_client()
            try:
                resp = c.post(
                    f"/collections/{coll}/documents/search",
                    json={"query": f"search query {i}", "mode": "semantic"},
                )
                return resp.status_code
            finally:
                c.close()

        with ThreadPoolExecutor(max_workers=20) as pool:
            futures = [pool.submit(search, i) for i in range(100)]
            statuses = [f.result() for f in as_completed(futures)]

        assert all(s == 200 for s in statuses)

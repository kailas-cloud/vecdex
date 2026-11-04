"""Full HTTP -> Bedrock TLS mock -> shared Valkey contract checks."""
import hashlib
import os

import httpx
import pytest

MOCK_URL = os.getenv("VECDEX_BEDROCK_MOCK_URL")
pytestmark = [pytest.mark.p0, pytest.mark.skipif(not MOCK_URL, reason="Bedrock mock profile only")]


def digest(text):
    return hashlib.sha256(text.encode()).hexdigest()


def matching_calls(hashes):
    response = httpx.get(f"{MOCK_URL}/_bedrock/calls", timeout=30)
    assert response.status_code == 200
    return [call for call in response.json()["calls"] if any(value in hashes for value in call["hashes"])]


def test_long_v4_input_is_sent_whole_and_query_has_own_purpose(client, collection_factory):
    name = collection_factory()["name"]
    content = f"{name}\n" + "English русский 中文 🚀 code x=1;\n" * 400
    assert len(content) // 4 > 512
    assert len(content) < 32768
    path = f"/collections/{name}/documents/whole"
    response = client.put(path, json={"content": content})
    assert response.status_code == 201, response.text
    assert client.get(path).json()["content"] == content
    calls = matching_calls({digest(content)})
    assert len(calls) == 1
    assert calls[0]["lengths"] == [len(content)]
    assert calls[0]["purpose"] == "search_document"
    assert calls[0]["max_tokens"] == 128000
    assert calls[0]["truncate"] == "NONE"
    query = f"{name} query"
    found = client.post(f"/collections/{name}/documents/search", json={"query": query, "mode": "semantic"})
    assert found.status_code == 200, found.text
    assert matching_calls({digest(query)})[0]["purpose"] == "search_query"


def test_native_97_batch_preserves_order_and_96_input_limit(client, collection_factory):
    name = collection_factory()["name"]
    documents = [{"id": f"bedrock-{i:03d}", "content": f"{name} complete source {i}"} for i in range(97)]
    response = client.post(f"/collections/{name}/documents/batch-upsert", json={"documents": documents})
    assert response.status_code == 200, response.text
    assert response.json()["succeeded"] == 97
    assert [item["id"] for item in response.json()["items"]] == [item["id"] for item in documents]
    hashes = [digest(item["content"]) for item in documents]
    calls = matching_calls(set(hashes))
    assert [len(call["hashes"]) for call in calls] == [96, 1]
    assert [value for call in calls for value in call["hashes"]] == hashes
    for index in (0, 95, 96):
        stored = client.get(f"/collections/{name}/documents/{documents[index]['id']}")
        assert stored.json()["content"] == documents[index]["content"]


@pytest.mark.parametrize("marker", ["MOCK_NO_TOKEN_HEADER", "MOCK_BAD_TOKEN_HEADER"])
def test_unknown_usage_keeps_strict_reservation_and_cache_hit_is_free(client, collection_factory, marker):
    name = collection_factory()["name"]
    content = f"{name} {marker} complete source"
    before = client.get("/usage", params={"period": "day"}).json()
    response = client.put(f"/collections/{name}/documents/first", json={"content": content})
    assert response.status_code == 201, response.text
    assert response.headers["x-embedding-usage"] == "unknown"
    assert "x-embedding-tokens" not in response.headers
    after = client.get("/usage", params={"period": "day"}).json()
    assert after["usage"]["unknown_requests"] == before["usage"]["unknown_requests"] + 1
    assert after["budget"]["tokens_remaining"] <= before["budget"]["tokens_remaining"] - 128000
    cached = client.put(f"/collections/{name}/documents/second", json={"content": content})
    assert cached.status_code == 201, cached.text
    assert cached.headers["x-embedding-tokens"] == "0"
    assert len(matching_calls({digest(content)})) == 1


def test_two_replicas_read_one_shared_budget(client, collection_factory):
    peer_url = os.environ["VECDEX_PEER_URL"]
    name = collection_factory()["name"]
    with httpx.Client(base_url=peer_url, headers={"Authorization": "Bearer test-api-key"}, timeout=330) as peer:
        content = f"{name} unique peer source"
        response = peer.put(f"/collections/{name}/documents/from-peer", json={"content": content})
        assert response.status_code == 201, response.text
        assert client.get(f"/collections/{name}/documents/from-peer").json()["content"] == content
        primary_report = client.get("/usage", params={"period": "day"}).json()
        peer_report = peer.get("/usage", params={"period": "day"}).json()
        assert peer_report["usage"] == primary_report["usage"]
        assert peer_report["budget"] == primary_report["budget"]


def test_later_native_batch_failure_keeps_completed_prefix(client, collection_factory):
    name = collection_factory()["name"]
    documents = [{"id": f"prefix-{i:03d}", "content": f"{name} completed prefix {i}"} for i in range(96)]
    documents.append({"id": "failed", "content": f"{name} MOCK_FAIL_FRAGMENT"})
    response = client.post(f"/collections/{name}/documents/batch-upsert", json={"documents": documents})
    assert response.status_code == 200, response.text
    assert response.json()["succeeded"] == 96
    assert response.json()["failed"] == 1
    assert client.get(f"/collections/{name}/documents/prefix-095").status_code == 200
    assert client.get(f"/collections/{name}/documents/failed").status_code == 404

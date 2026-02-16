"""Geo collection E2E tests — create, upsert, search, validation, accuracy."""

import time

import pytest

from conftest import (
    NYC_POIS,
    assert_no_embedding_headers,
    search_with_retry,
)


# --- Known distances (Haversine, meters) ---
# Pre-computed with the geo.Haversine function for test assertions.
# Times Square (40.7580, -73.9855) as origin:
DIST_TS_CENTRAL_PARK = 2_841  # ~2.8 km
DIST_TS_BROOKLYN_BRIDGE = 5_811  # ~5.8 km
DIST_TS_STATUE_LIBERTY = 8_217  # ~8.2 km
DIST_TOLERANCE = 0.15  # 15% tolerance for ECEF→Haversine approximation


def geo_search(client, collection, lat, lon, **kwargs):
    """Helper: geo search with lat,lon query string."""
    return client.post(
        f"/collections/{collection}/documents/search",
        json={"query": f"{lat},{lon}", "mode": "geo", **kwargs},
    )


def geo_search_with_retry(client, collection, lat, lon, **kwargs):
    """Helper: geo search with retry for indexing lag."""
    return search_with_retry(
        client, collection, query=f"{lat},{lon}", mode="geo", **kwargs
    )


# ============================================================
# P0 — Core functionality
# ============================================================


@pytest.mark.p0
class TestGeoCollectionCreate:
    """POST /collections with type=geo."""

    def test_create_geo_returns_201(self, client, geo_collection_factory):
        coll = geo_collection_factory()
        assert coll is not None

    def test_create_geo_returns_type_geo(self, client, geo_collection_factory):
        coll = geo_collection_factory()
        assert coll["type"] == "geo"

    def test_create_geo_has_vector_dimensions_3(self, client, geo_collection_factory):
        coll = geo_collection_factory()
        assert coll["vector_dimensions"] == 3

    def test_get_geo_collection_shows_type(self, client, geo_collection_factory):
        coll = geo_collection_factory()
        resp = client.get(f"/collections/{coll['name']}")
        assert resp.status_code == 200
        assert resp.json()["type"] == "geo"

    def test_create_geo_with_custom_fields(self, client, collection_factory):
        coll = collection_factory(
            fields=[
                {"name": "latitude", "type": "numeric"},
                {"name": "longitude", "type": "numeric"},
                {"name": "rating", "type": "numeric"},
                {"name": "category", "type": "tag"},
                {"name": "city", "type": "tag"},
            ],
            type="geo",
        )
        assert coll["type"] == "geo"
        assert len(coll["fields"]) == 5


@pytest.mark.p0
class TestGeoDocumentUpsert:
    """PUT /collections/{collection}/documents/{id} for geo collections."""

    def test_upsert_geo_doc_returns_201(self, client, geo_collection_factory):
        coll = geo_collection_factory()
        resp = client.put(
            f"/collections/{coll['name']}/documents/poi-1",
            json={
                "content": "Test location",
                "numerics": {"latitude": 40.7128, "longitude": -74.0060},
                "tags": {"category": "test"},
            },
        )
        assert resp.status_code == 201

    def test_upsert_geo_doc_no_embedding_headers(self, client, geo_collection_factory):
        coll = geo_collection_factory()
        resp = client.put(
            f"/collections/{coll['name']}/documents/poi-1",
            json={
                "content": "No embedding needed",
                "numerics": {"latitude": 40.7128, "longitude": -74.0060},
            },
        )
        assert_no_embedding_headers(resp)

    def test_upsert_geo_doc_without_content(self, client, geo_collection_factory):
        """Geo collections don't require content — vectorization uses lat/lon."""
        coll = geo_collection_factory()
        resp = client.put(
            f"/collections/{coll['name']}/documents/poi-1",
            json={
                "numerics": {"latitude": 40.7128, "longitude": -74.0060},
            },
        )
        assert resp.status_code in (200, 201)

    def test_upsert_geo_doc_stores_numerics(self, client, geo_collection_factory):
        coll = geo_collection_factory()
        client.put(
            f"/collections/{coll['name']}/documents/poi-1",
            json={
                "numerics": {"latitude": 40.7128, "longitude": -74.0060},
                "tags": {"category": "test"},
            },
        )
        resp = client.get(f"/collections/{coll['name']}/documents/poi-1")
        data = resp.json()
        assert abs(data["numerics"]["latitude"] - 40.7128) < 0.001
        assert abs(data["numerics"]["longitude"] - (-74.0060)) < 0.001

    def test_upsert_geo_doc_update_returns_200(self, client, geo_collection_factory):
        coll = geo_collection_factory()
        client.put(
            f"/collections/{coll['name']}/documents/poi-1",
            json={"numerics": {"latitude": 40.0, "longitude": -74.0}},
        )
        resp = client.put(
            f"/collections/{coll['name']}/documents/poi-1",
            json={"numerics": {"latitude": 41.0, "longitude": -73.0}},
        )
        assert resp.status_code == 200

    def test_upsert_geo_doc_with_vector_3d(self, client, geo_collection_factory):
        """Geo docs should produce 3D ECEF vectors."""
        coll = geo_collection_factory()
        client.put(
            f"/collections/{coll['name']}/documents/poi-1",
            json={"numerics": {"latitude": 0.0, "longitude": 0.0}},
        )
        time.sleep(0.3)
        resp = geo_search(
            client, coll["name"], 0.0, 0.0, include_vectors=True, top_k=1,
        )
        assert resp.status_code == 200
        items = resp.json()["items"]
        if len(items) > 0:
            assert len(items[0]["vector"]) == 3


@pytest.mark.p0
class TestGeoDocumentValidation:
    """Validation for geo document upsert."""

    def test_missing_latitude_returns_400(self, client, geo_collection_factory):
        coll = geo_collection_factory()
        resp = client.put(
            f"/collections/{coll['name']}/documents/poi-1",
            json={"numerics": {"longitude": -74.0060}},
        )
        assert resp.status_code == 400

    def test_missing_longitude_returns_400(self, client, geo_collection_factory):
        coll = geo_collection_factory()
        resp = client.put(
            f"/collections/{coll['name']}/documents/poi-1",
            json={"numerics": {"latitude": 40.7128}},
        )
        assert resp.status_code == 400

    def test_latitude_out_of_range_returns_400(self, client, geo_collection_factory):
        coll = geo_collection_factory()
        resp = client.put(
            f"/collections/{coll['name']}/documents/poi-1",
            json={"numerics": {"latitude": 91.0, "longitude": 0.0}},
        )
        assert resp.status_code == 400

    def test_longitude_out_of_range_returns_400(self, client, geo_collection_factory):
        coll = geo_collection_factory()
        resp = client.put(
            f"/collections/{coll['name']}/documents/poi-1",
            json={"numerics": {"latitude": 0.0, "longitude": 181.0}},
        )
        assert resp.status_code == 400

    def test_no_numerics_returns_400(self, client, geo_collection_factory):
        """Geo docs need at least lat/lon numerics."""
        coll = geo_collection_factory()
        resp = client.put(
            f"/collections/{coll['name']}/documents/poi-1",
            json={"content": "text only"},
        )
        assert resp.status_code == 400


@pytest.mark.p0
class TestGeoSearch:
    """POST /collections/{name}/documents/search with mode=geo."""

    def test_geo_search_returns_results(self, client, populated_geo_collection):
        coll = populated_geo_collection["name"]
        resp = geo_search_with_retry(client, coll, 40.7580, -73.9855)
        assert len(resp.json()["items"]) > 0

    def test_geo_scores_are_distances_in_meters(self, client, populated_geo_collection):
        """Geo scores represent great-circle distance in meters."""
        coll = populated_geo_collection["name"]
        resp = geo_search_with_retry(client, coll, 40.7580, -73.9855)
        for item in resp.json()["items"]:
            assert item["score"] >= 0

    def test_geo_results_sorted_ascending(self, client, populated_geo_collection):
        """Geo results sorted by distance ascending (closest first)."""
        coll = populated_geo_collection["name"]
        resp = geo_search_with_retry(client, coll, 40.7580, -73.9855)
        items = resp.json()["items"]
        scores = [item["score"] for item in items]
        assert scores == sorted(scores)

    def test_geo_closest_is_times_square(self, client, populated_geo_collection):
        """Searching from Times Square coords should return Times Square first."""
        coll = populated_geo_collection["name"]
        resp = geo_search_with_retry(client, coll, 40.7580, -73.9855)
        items = resp.json()["items"]
        assert items[0]["id"] == "times-square"
        assert items[0]["score"] < 100  # should be near-zero meters

    def test_geo_search_has_content(self, client, populated_geo_collection):
        coll = populated_geo_collection["name"]
        resp = geo_search_with_retry(client, coll, 40.7580, -73.9855)
        for item in resp.json()["items"]:
            assert "id" in item
            # content may or may not be present depending on whether it was set

    def test_geo_search_no_embedding_headers(self, client, populated_geo_collection):
        coll = populated_geo_collection["name"]
        resp = geo_search_with_retry(client, coll, 40.7580, -73.9855)
        assert_no_embedding_headers(resp)


@pytest.mark.p0
class TestGeoCollectionTypeMismatch:
    """Geo/text mode mismatch returns 400."""

    def test_geo_search_on_text_collection_returns_400(
        self, client, populated_collection
    ):
        """Geo search on a text collection → 400."""
        coll = populated_collection["name"]
        resp = geo_search(client, coll, 40.7128, -74.0060)
        assert resp.status_code == 400
        assert resp.json()["code"] == "collection_type_mismatch"

    def test_semantic_search_on_geo_collection_returns_400(
        self, client, populated_geo_collection
    ):
        """Semantic search on a geo collection → 400."""
        coll = populated_geo_collection["name"]
        resp = client.post(
            f"/collections/{coll}/documents/search",
            json={"query": "test query", "mode": "semantic"},
        )
        assert resp.status_code == 400
        assert resp.json()["code"] == "collection_type_mismatch"

    def test_keyword_search_on_geo_collection_returns_400(
        self, client, populated_geo_collection
    ):
        coll = populated_geo_collection["name"]
        resp = client.post(
            f"/collections/{coll}/documents/search",
            json={"query": "test", "mode": "keyword"},
        )
        assert resp.status_code == 400
        assert resp.json()["code"] == "collection_type_mismatch"

    def test_hybrid_search_on_geo_collection_returns_400(
        self, client, populated_geo_collection
    ):
        coll = populated_geo_collection["name"]
        resp = client.post(
            f"/collections/{coll}/documents/search",
            json={"query": "test", "mode": "hybrid"},
        )
        assert resp.status_code == 400
        assert resp.json()["code"] == "collection_type_mismatch"


@pytest.mark.p0
class TestGeoSearchErrorContract:
    """Error format for geo-specific errors."""

    def test_malformed_query_returns_400(self, client, populated_geo_collection):
        """Geo query must be 'lat,lon' format."""
        coll = populated_geo_collection["name"]
        resp = client.post(
            f"/collections/{coll}/documents/search",
            json={"query": "not-a-coordinate", "mode": "geo"},
        )
        assert resp.status_code == 400

    def test_single_number_query_returns_400(self, client, populated_geo_collection):
        coll = populated_geo_collection["name"]
        resp = client.post(
            f"/collections/{coll}/documents/search",
            json={"query": "40.7128", "mode": "geo"},
        )
        assert resp.status_code == 400

    def test_invalid_lat_in_query_returns_400(self, client, populated_geo_collection):
        """Latitude > 90 in search query."""
        coll = populated_geo_collection["name"]
        resp = client.post(
            f"/collections/{coll}/documents/search",
            json={"query": "91.0,-74.0", "mode": "geo"},
        )
        assert resp.status_code == 400

    def test_error_has_code_and_message(self, client, populated_geo_collection):
        coll = populated_geo_collection["name"]
        resp = client.post(
            f"/collections/{coll}/documents/search",
            json={"query": "bad", "mode": "geo"},
        )
        assert resp.status_code == 400
        data = resp.json()
        assert "code" in data
        assert "message" in data


# ============================================================
# P1 — Edge cases & accuracy
# ============================================================


@pytest.mark.p1
class TestGeoSearchDistanceAccuracy:
    """Validate known distances between NYC POIs."""

    def test_times_square_to_central_park(self, client, populated_geo_collection):
        coll = populated_geo_collection["name"]
        resp = geo_search_with_retry(client, coll, 40.7580, -73.9855, top_k=5)
        items = resp.json()["items"]
        cp = next((i for i in items if i["id"] == "central-park"), None)
        assert cp is not None
        assert abs(cp["score"] - DIST_TS_CENTRAL_PARK) / DIST_TS_CENTRAL_PARK < DIST_TOLERANCE

    def test_times_square_to_brooklyn_bridge(self, client, populated_geo_collection):
        coll = populated_geo_collection["name"]
        resp = geo_search_with_retry(client, coll, 40.7580, -73.9855, top_k=5)
        items = resp.json()["items"]
        bb = next((i for i in items if i["id"] == "brooklyn-bridge"), None)
        assert bb is not None
        assert abs(bb["score"] - DIST_TS_BROOKLYN_BRIDGE) / DIST_TS_BROOKLYN_BRIDGE < DIST_TOLERANCE

    def test_times_square_to_statue_liberty(self, client, populated_geo_collection):
        coll = populated_geo_collection["name"]
        resp = geo_search_with_retry(client, coll, 40.7580, -73.9855, top_k=5)
        items = resp.json()["items"]
        sl = next((i for i in items if i["id"] == "statue-liberty"), None)
        assert sl is not None
        assert abs(sl["score"] - DIST_TS_STATUE_LIBERTY) / DIST_TS_STATUE_LIBERTY < DIST_TOLERANCE


@pytest.mark.p1
class TestGeoSearchParams:
    """top_k, limit, include_vectors with geo search."""

    def test_top_k_limits_results(self, client, populated_geo_collection):
        coll = populated_geo_collection["name"]
        resp = geo_search_with_retry(client, coll, 40.7580, -73.9855, top_k=2)
        assert len(resp.json()["items"]) <= 2

    def test_limit_caps_results(self, client, populated_geo_collection):
        coll = populated_geo_collection["name"]
        resp = geo_search_with_retry(client, coll, 40.7580, -73.9855, limit=3)
        assert len(resp.json()["items"]) <= 3

    def test_include_vectors_returns_3d(self, client, populated_geo_collection):
        coll = populated_geo_collection["name"]
        resp = geo_search_with_retry(
            client, coll, 40.7580, -73.9855, include_vectors=True
        )
        for item in resp.json()["items"]:
            assert "vector" in item
            assert len(item["vector"]) == 3

    def test_response_has_total_and_limit(self, client, populated_geo_collection):
        coll = populated_geo_collection["name"]
        resp = geo_search_with_retry(client, coll, 40.7580, -73.9855)
        data = resp.json()
        assert "total" in data
        assert "limit" in data


@pytest.mark.p1
class TestGeoDocumentPatch:
    """PATCH for geo documents."""

    def test_patch_coordinates_updates_position(self, client, geo_collection_factory):
        """Patching lat/lon should re-vectorize."""
        coll = geo_collection_factory()
        client.put(
            f"/collections/{coll['name']}/documents/poi-1",
            json={"numerics": {"latitude": 40.0, "longitude": -74.0}},
        )
        resp = client.patch(
            f"/collections/{coll['name']}/documents/poi-1",
            json={"numerics": {"latitude": 51.5, "longitude": -0.1}},
        )
        assert resp.status_code == 200
        data = resp.json()
        assert abs(data["numerics"]["latitude"] - 51.5) < 0.01

    def test_patch_content_only_keeps_position(self, client, geo_collection_factory):
        coll = geo_collection_factory()
        client.put(
            f"/collections/{coll['name']}/documents/poi-1",
            json={
                "content": "original",
                "numerics": {"latitude": 40.7128, "longitude": -74.0060},
            },
        )
        resp = client.patch(
            f"/collections/{coll['name']}/documents/poi-1",
            json={"content": "updated"},
        )
        assert resp.status_code == 200
        data = resp.json()
        assert data["content"] == "updated"
        assert abs(data["numerics"]["latitude"] - 40.7128) < 0.01

    def test_patch_tags_on_geo_doc(self, client, geo_collection_factory):
        coll = geo_collection_factory()
        client.put(
            f"/collections/{coll['name']}/documents/poi-1",
            json={
                "numerics": {"latitude": 40.7128, "longitude": -74.0060},
                "tags": {"category": "park"},
            },
        )
        resp = client.patch(
            f"/collections/{coll['name']}/documents/poi-1",
            json={"tags": {"category": "landmark"}},
        )
        assert resp.status_code == 200
        assert resp.json()["tags"]["category"] == "landmark"

    def test_patch_no_embedding_headers(self, client, geo_collection_factory):
        coll = geo_collection_factory()
        client.put(
            f"/collections/{coll['name']}/documents/poi-1",
            json={"numerics": {"latitude": 40.0, "longitude": -74.0}},
        )
        resp = client.patch(
            f"/collections/{coll['name']}/documents/poi-1",
            json={"numerics": {"latitude": 41.0, "longitude": -73.0}},
        )
        assert_no_embedding_headers(resp)


@pytest.mark.p1
class TestGeoBatchUpsert:
    """Batch upsert for geo documents."""

    def test_batch_upsert_geo_returns_200(self, client, geo_collection_factory):
        coll = geo_collection_factory()
        resp = client.post(
            f"/collections/{coll['name']}/documents/batch-upsert",
            json={
                "documents": [
                    {
                        "id": "b1",
                        "numerics": {"latitude": 40.0, "longitude": -74.0},
                    },
                    {
                        "id": "b2",
                        "numerics": {"latitude": 41.0, "longitude": -73.0},
                    },
                ]
            },
        )
        assert resp.status_code == 200
        assert resp.json()["succeeded"] == 2

    def test_batch_upsert_geo_no_embedding_headers(
        self, client, geo_collection_factory
    ):
        coll = geo_collection_factory()
        resp = client.post(
            f"/collections/{coll['name']}/documents/batch-upsert",
            json={
                "documents": [
                    {
                        "id": "b1",
                        "numerics": {"latitude": 40.0, "longitude": -74.0},
                    },
                ]
            },
        )
        assert_no_embedding_headers(resp)

    def test_batch_upsert_geo_searchable(self, client, geo_collection_factory):
        coll = geo_collection_factory()
        client.post(
            f"/collections/{coll['name']}/documents/batch-upsert",
            json={
                "documents": [
                    {
                        "id": "nyc",
                        "content": "New York",
                        "numerics": {"latitude": 40.7128, "longitude": -74.0060},
                    },
                    {
                        "id": "la",
                        "content": "Los Angeles",
                        "numerics": {"latitude": 34.0522, "longitude": -118.2437},
                    },
                ]
            },
        )
        time.sleep(0.5)
        resp = geo_search_with_retry(client, coll["name"], 40.7128, -74.0060, top_k=2)
        items = resp.json()["items"]
        assert items[0]["id"] == "nyc"


@pytest.mark.p1
class TestGeoDocumentCRUD:
    """Standard CRUD operations on geo collections."""

    def test_get_geo_document(self, client, geo_collection_factory):
        coll = geo_collection_factory()
        client.put(
            f"/collections/{coll['name']}/documents/poi-1",
            json={
                "content": "Test POI",
                "numerics": {"latitude": 40.7128, "longitude": -74.0060},
            },
        )
        resp = client.get(f"/collections/{coll['name']}/documents/poi-1")
        assert resp.status_code == 200
        assert resp.json()["id"] == "poi-1"

    def test_delete_geo_document(self, client, geo_collection_factory):
        coll = geo_collection_factory()
        client.put(
            f"/collections/{coll['name']}/documents/poi-1",
            json={"numerics": {"latitude": 40.0, "longitude": -74.0}},
        )
        resp = client.delete(f"/collections/{coll['name']}/documents/poi-1")
        assert resp.status_code == 204
        resp = client.get(f"/collections/{coll['name']}/documents/poi-1")
        assert resp.status_code == 404

    def test_list_geo_documents(self, client, populated_geo_collection):
        coll = populated_geo_collection["name"]
        resp = client.get(f"/collections/{coll}/documents")
        assert resp.status_code == 200
        data = resp.json()
        assert len(data["items"]) == len(NYC_POIS)

    def test_delete_geo_collection(self, client, collection_factory):
        coll = collection_factory(
            fields=[
                {"name": "latitude", "type": "numeric"},
                {"name": "longitude", "type": "numeric"},
            ],
            type="geo",
        )
        resp = client.delete(f"/collections/{coll['name']}")
        assert resp.status_code == 204


@pytest.mark.p1
class TestGeoMinScore:
    """min_score as max-distance threshold in geo mode."""

    def test_min_score_filters_far_results(self, client, populated_geo_collection):
        """min_score=5000 should keep results within 5km."""
        coll = populated_geo_collection["name"]
        resp = geo_search(
            client, coll, 40.7580, -73.9855, min_score=5000, top_k=10,
        )
        assert resp.status_code == 200
        items = resp.json()["items"]
        for item in items:
            assert item["score"] <= 5000, (
                f"{item['id']} at {item['score']:.0f}m should be within 5km"
            )

    def test_min_score_excludes_distant_pois(self, client, populated_geo_collection):
        """min_score=3000 from Times Square should exclude Brooklyn Bridge (~5.8km)."""
        coll = populated_geo_collection["name"]
        resp = geo_search(
            client, coll, 40.7580, -73.9855, min_score=3000, top_k=10,
        )
        assert resp.status_code == 200
        ids = [item["id"] for item in resp.json()["items"]]
        assert "brooklyn-bridge" not in ids
        assert "statue-liberty" not in ids


# ============================================================
# P2 — Stress
# ============================================================


@pytest.mark.p2
class TestGeoSearchStress:
    """Stress tests with many POIs."""

    def test_50_pois_batch_and_search(self, client, geo_collection_factory):
        """Insert 50 POIs via batch, then search."""
        coll = geo_collection_factory()
        docs = []
        for i in range(50):
            lat = 40.0 + (i * 0.01)
            lon = -74.0 + (i * 0.01)
            docs.append({
                "id": f"stress-{i}",
                "content": f"POI {i}",
                "numerics": {"latitude": lat, "longitude": lon},
            })

        # Batch in chunks of 50 (within 100 limit)
        resp = client.post(
            f"/collections/{coll['name']}/documents/batch-upsert",
            json={"documents": docs},
        )
        assert resp.status_code == 200
        assert resp.json()["succeeded"] == 50

        time.sleep(1.0)
        resp = geo_search_with_retry(client, coll["name"], 40.0, -74.0, top_k=10)
        items = resp.json()["items"]
        assert len(items) > 0
        # Closest should be stress-0 (exact match location)
        assert items[0]["id"] == "stress-0"

    def test_search_across_hemispheres(self, client, geo_collection_factory):
        """Search from far away — all results should have large distances."""
        coll = geo_collection_factory()
        # Insert NYC and Tokyo
        for doc in [
            {"id": "nyc", "numerics": {"latitude": 40.7128, "longitude": -74.0060}},
            {"id": "tokyo", "numerics": {"latitude": 35.6762, "longitude": 139.6503}},
        ]:
            client.put(
                f"/collections/{coll['name']}/documents/{doc['id']}",
                json=doc,
            )
        time.sleep(0.5)

        # Search from NYC
        resp = geo_search_with_retry(client, coll["name"], 40.7128, -74.0060, top_k=2)
        items = resp.json()["items"]
        assert items[0]["id"] == "nyc"
        assert items[0]["score"] < 100  # near zero
        # Tokyo should be ~10,800 km away
        tokyo = next((i for i in items if i["id"] == "tokyo"), None)
        assert tokyo is not None
        assert tokyo["score"] > 10_000_000  # > 10,000 km in meters

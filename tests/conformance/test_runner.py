"""A documented fixture gap must never hide an actual protocol regression."""

import unittest

from run import classify_check


class ClassificationTests(unittest.TestCase):
    def test_only_exact_untestable_failure_is_a_fixture_limitation(self):
        manifest = {"fixture_limitations": {"scenario": {"probe": "test-only tool absent"}}, "optional_skips": {}}
        check = {"id": "probe", "status": "FAILURE", "details": {"untestable": True}}
        self.assertEqual(classify_check("scenario", check, manifest), "fixture_limitation")
        self.assertEqual(classify_check("other", check, manifest), "failed")
        check["details"] = {"response": {"error": "real regression"}}
        self.assertEqual(classify_check("scenario", check, manifest), "failed")
        check["status"] = "SKIPPED"
        self.assertEqual(classify_check("scenario", check, manifest), "failed")

    def test_undeclared_optional_skips_are_explicit(self):
        manifest = {"fixture_limitations": {}, "optional_skips": {"scenario": ["optional"]}}
        self.assertEqual(classify_check("scenario", {"id": "optional", "status": "SKIPPED"}, manifest),
                         "optional_not_advertised")
        self.assertEqual(classify_check("scenario", {"id": "new", "status": "SKIPPED"}, manifest), "failed")
        self.assertEqual(classify_check("scenario", {"id": "optional", "status": "WARNING"}, manifest), "failed")

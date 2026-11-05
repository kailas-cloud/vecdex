"""The strict engine xfail must reject unrelated or incomplete crash evidence."""
import pytest

from test_topology_consistency import consistency_abort_signature


ARM_STACK = """valkey 9.1.1 crashed by signal: 6, si_code: -6
/usr/lib/valkey/libsearch.so(LogMessageFatalD1Ev+0x9) [0x1000]
/usr/lib/valkey/libsearch.so(PerformSlotConsistencyCheck+0x16c) [0x2000]
/usr/lib/valkey/libsearch.so(SearchIndexPartition+0x548) [0x3000]
"""
AMD_STACK = """valkey 9.1.1 crashed by signal: 6, si_code: -6
/usr/lib/valkey/libsearch.so(_ZN4absl12lts_2024072212log_internal15LogMessageFatalD1Ev+0x9) [0x7fcd907689a9]
/usr/lib/valkey/libsearch.so(+0x3c725e) [0x7fcd901b525e]
/usr/lib/valkey/libsearch.so(_ZN13valkey_search11coordinator7Service20SearchIndexPartitionEPN4grpc21CallbackServerContextEPKNS0_27SearchIndexPartitionRequestEPNS0_28SearchIndexPartitionResponseE+0x555) [0x7fcd9048e895]
"""


@pytest.mark.parametrize(("log", "expected"), [
    (ARM_STACK, "symbolized-consistency-check"),
    (AMD_STACK, "pinned-amd64-inlined-consistency-check"),
    (ARM_STACK.replace("signal: 6", "signal: 11"), None),
    (AMD_STACK.replace("signal: 6", "signal: 11"), None),
    (AMD_STACK.replace("0x3c725e", "0x3c725f"), None),
    (AMD_STACK.replace("+0x555)", "+0x556)"), None),
    (AMD_STACK.replace("LogMessageFatalD1Ev", "AnotherFailure"), None),
    (ARM_STACK.replace("PerformSlotConsistencyCheck", "AnotherAssertion"), None),
    (AMD_STACK.replace("SearchIndexPartition", "AnotherRpc"), None),
    ("connection refused", None),
], ids=["arm64", "amd64", "arm64-other-signal", "amd64-other-signal",
        "other-cold-frame", "other-rpc-offset", "missing-fatal", "other-assertion",
        "other-rpc", "transport-error"])
def test_crash_signature_requires_the_known_official_frames(log, expected):
    assert consistency_abort_signature(log) == expected

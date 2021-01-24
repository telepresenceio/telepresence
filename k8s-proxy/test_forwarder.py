"""
Tests for ``resolver``, a forwarding DNS resolver which enables in-Pod-like
name resolution for anyone who sends queries to it.
"""

from itertools import count
from string import ascii_lowercase

import pytest

from twisted.names.dns import (
    Record_A,
    RRHeader,
    Query,
)

from resolver import (
    LocalResolver,
    insort,
)

from hypothesis import strategies as st, given


@pytest.fixture
def resolver():
    return LocalResolver(
        # Construct it with an invalid DNS server address (an IP is required,
        # no hostnames allowed).  We're not interested in actually issuing DNS
        # queries to any servers during these tests.  This will ensure that if
        # we attempt to do so, it will break quickly.
        b"example.invalid",
        u"default",
    )


def test_infer_search_domains(resolver):
    """
    ``LocalResolver`` uses a number of DNS queries sent to it as probes to
    infer the search domains configured for the client.
    """
    probe = u"hellotelepresence"
    counter = count(0)
    for search in [u".foo", u".foo.bar", u".alternate"]:
        for i in range(3):
            name = u"{}{}{}".format(
                probe, next(counter), search,
            ).encode("ascii")
            rrheader = RRHeader(
                name=name,
                payload=Record_A(address=b"127.0.0.1"),
            )
            expected = ([rrheader], [], [])
            result = resolver.query(Query(name))
            assert expected == result

    for search in [u".foo", u".foo.bar", u".alternate"]:
        mangled = (u"example.com" + search).encode("ascii").split(b".")
        assert [b"example", b"com"] == resolver._strip_search_suffix(mangled)


def labels():
    """
    Build random DNS labels.

    This can't build every possible DNS label.  It just does enough to
    exercise the suffix detection logic (assuming that logic is independent of
    the particular bytes of the labels).
    """
    return st.text(
        alphabet=ascii_lowercase,
        min_size=1,
    )


@given(labels(), labels(), labels())
def test_prefer_longest_suffix(first, second, third):
    """
    If ``LocalResolver`` observes overlapping suffixes (for example, "foo" and
    "bar.foo") then it prefers to strip the longest one possible from any
    queries it forwards.
    """
    # See the resolver fixture defined above. Not using a fixture here because
    # that only gets run once per test function, and we want this to run once
    # per Hypothesis call.
    resolver = LocalResolver(b"example.invalid", u"default")

    probe = "hellotelepresence"
    target_suffix = "{}.{}".format(second, third)

    # Let it discover a few overlapping suffixes.
    resolver.query(
        Query("{}.{}".format(probe, third).encode("ascii")),
    )

    resolver.query(
        Query("{}.{}".format(probe, target_suffix).encode("ascii")),
    )

    resolver.query(
        Query("{}.{}.{}.{}".format(
            probe, first, second, third,
        ).encode("ascii")),
    )

    # Ask it what base name it would forward if it received a query for a name
    # which has both suffixes.  We would like it to strip the longest prefix
    # it can.
    stripped = resolver._strip_search_suffix(
        "example.{}".format(target_suffix).encode("ascii").split(b"."),
    )
    assert [b"example"] == stripped


@given(st.lists(st.integers()))
def test_insort(values):
    """
    ``insort`` inserts a new element into a sorted list in the correct
    position to maintain the list's sorted property.
    """
    insort_target = []
    for v in values:
        insort(insort_target, v, key=lambda v: -v)

    assert sorted(values, reverse=True) == insort_target

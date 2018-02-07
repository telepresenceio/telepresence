"""
Tests for ``resolver``, a forwarding DNS resolver which enables in-Pod-like
name resolution for anyone who sends queries to it.
"""

from itertools import count

import pytest

from twisted.names.dns import (
    Record_A,
    RRHeader,
    Query,
)

from resolver import LocalResolver


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
            name = u"{}{}{}".format(probe, next(counter), search).encode("ascii")
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

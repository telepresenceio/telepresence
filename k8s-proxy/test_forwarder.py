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
    return LocalResolver(True, b"example.invalid", u"default")


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
            expected = ([RRHeader(name, Record_A(b"127.0.0.1"))], [], [])

            result = resolver.query(Query(name))
            assert expected == result

    for search in [u".foo", u".foo.bar", u".alternate"]:
        assert b"example.com" == resolver._strip_search_suffix(b"example.com" + search)

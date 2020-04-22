import pytest

from twisted.names import dns
from unittest import mock

from resolver import LocalResolver


@pytest.fixture
def resolver():
    return LocalResolver("8.8.8.8", "my-ns")


class TestLocalResolver:
    @staticmethod
    @mock.patch("resolver.client")
    def test_query_service(client_mock: mock.Mock, resolver: LocalResolver):
        """
        Service name must be completed with namespace and .svc.cluster.local.
        """
        query = dns.Query("my-service")
        resolver.query(query)
        client_mock.Resolver.return_value.query.assert_called_with(
            dns.Query("my-service.my-ns.svc.cluster.local", dns.A, mock.ANY),
            timeout=mock.ANY,
        )

    @staticmethod
    @mock.patch("resolver.client")
    def test_query_service_ns(client_mock: mock.Mock, resolver: LocalResolver):
        """
        Service name + namespace must be completed with .svc.cluster.local.
        """
        query = dns.Query("service.given-ns")
        resolver.query(query)
        client_mock.Resolver.return_value.query.assert_called_with(
            dns.Query("service.given-ns.svc.cluster.local", dns.A, mock.ANY),
            timeout=mock.ANY,
        )

    @staticmethod
    @mock.patch("resolver.client")
    def test_query_svc(client_mock: mock.Mock, resolver: LocalResolver):
        """.svc host must be completed with .cluster.local."""
        query = dns.Query("some-pod.my-service.ns.svc")
        resolver.query(query)
        client_mock.Resolver.return_value.query.assert_called_with(
            dns.Query(
                "some-pod.my-service.ns.svc.cluster.local", dns.A, mock.ANY
            ),
            timeout=mock.ANY,
        )

    @staticmethod
    @mock.patch("resolver.client")
    def test_query_local(client_mock: mock.Mock, resolver: LocalResolver):
        """.local host must be left un-touched."""
        query = dns.Query("leave-me.local")
        resolver.query(query)
        client_mock.Resolver.return_value.query.assert_called_with(
            dns.Query("leave-me.local", dns.A, mock.ANY), timeout=mock.ANY
        )

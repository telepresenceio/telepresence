"""
Configure pytest for the Telepresence end-to-end test suite.
"""

from itertools import product

import pytest

from .parameterize_utils import METHODS, OPERATION_GETTERS, Probe


# Mark this as the `probe` fixture and declare that instances of it may be
# shared by any tests within the same module.
@pytest.fixture(scope="module")
def probe(request):
    method, operation = request.param
    reason = method.unsupported()
    if reason is None:
        probe = Probe(request, method, operation)
        yield probe
        probe.cleanup()
    else:
        pytest.skip(reason)


def _make_mark(name):
    """
    Turn a string into a pytest mark.
    """
    return getattr(pytest.mark, name.replace("-", "_"))


def _get_marks(items):
    """
    Get the pytest marks appropriate for the given method and operation.
    """
    return [_make_mark(item.name) for item in items]


def _probe_parametrize(fixture_name):
    """
    Create a "parametrized" pytest fixture which will supply Probes (one for
    each coordinate in the cartesion space defined by METHODS and OPERATIONS)
    to test functions which use it.
    """
    param_values = [
        (method, op_getter())
        for method, op_getter in product(METHODS, OPERATION_GETTERS)
    ]
    return pytest.mark.parametrize(
        # Parameterize the probe parameter to decorated methods
        fixture_name,

        # The parameters are the elements of the cartesian product of methods,
        # operations.
        [
            pytest.param(value, marks=_get_marks(value))
            for value in param_values
        ],

        # Use the `name` of methods and operations to generate readable
        # parameterized test names.
        ids=lambda param: "{},{}".format(param[0].name, param[1].name),

        # Pass the parameters through the probe fixture to get the object
        # that's really passed to the decorated function.
        indirect=True,
    )


# Create a fixture supplying a Probe.
with_probe = _probe_parametrize("probe")

_after_probe_mark = pytest.mark.after_probe()


def after_probe(f):
    """
    Decorate a test method to supply the ``probe`` fixture but only after all
    ``with_probe``-decorated tests have had a chance to run for the same probe
    parameters.

    That is, all ``with_probe`` tests will run against the
    *Probe[container,new]* configuration and then all ``after_probe`` tests
    will run against that same configuration.  This allows
    ``after_probe``-decorated tests to make assertions about the state of the
    system after Telepresence exits.
    """
    return with_probe(_after_probe_mark(f))


def pytest_collection_modifyitems(session, config, items):
    """
    Re-arrange the test items so that ``after_probe`` tests run immediately
    after ``with_probe`` tests that share the same parameters.

    This provides the desired semantics of the ``after_probe`` fixture - that
    such tests always run after any tests that use ``with_probe`` (with
    corresponding parameters).

    Additionally, we take care to make sure the ``after_probe`` tests appear
    *immediately* after the corresponding ``with_probe`` tests.  This is
    necessary so that ``after_probe`` tests can share the same probe as the
    corresponding ``with_probe`` tests.  If unrelated tests are allowed to
    intervene, the probe fixture may be cleaned up and then a new one
    allocated for the ``after_probe`` tests.
    """
    # Find all the tests that need to run after the probe is done.
    marked_items = [item for item in items if _is_after_probe_item(item)]

    # Remove them from the original collection list.
    items[:] = [item for item in items if item not in marked_items]
    # Put them back immediately after the other tests that use a probe with the
    # same params.
    for inserting in marked_items:
        found = False
        for i, existing in enumerate(items):
            try:
                callspec = existing.callspec
            except AttributeError:
                # Not all Functions have a callspec.  Any that doesn't isn't
                # parametrized like we are.  If we were previously within the
                # block of like-configured tests, we're not anymore.
                if found:
                    items.insert(i, inserting)
                    break

                # None of the rest of the logic of this loop makes sense for
                # non-parameterized tests.
                continue

            if callspec.params == inserting.callspec.params:
                # We're somewhere in the block of like-configured tests.  We
                # assume they all appear together.
                found = True
            elif found:
                # We just went past the end of the group of like-param tests.
                # Insert the test right here, pushing differently configured
                # stuff back.
                items.insert(i, inserting)
                break
        else:
            if found:
                # If we found the tests but we didn't insert it yet, this
                # block must be at the end of the list of items.  So just
                # append the subject test.
                items.append(inserting)
            else:
                # If we _didn't_ find the tests then something is weird.
                raise Exception(
                    "Could not find correct position for {}".format(inserting)
                )


def _is_after_probe_item(item):
    """
    Determine if a test item (Function) was marked with ``after_probe``.
    """
    return item.get_closest_marker("after_probe") is not None

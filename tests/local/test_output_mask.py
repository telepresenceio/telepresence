import json
from string import ascii_letters, printable

import pytest
import yaml
from hamcrest import assert_that, equal_to
from hypothesis import given, settings
from hypothesis import strategies as st

from telepresence.runner.output_mask import mask_sensitive_data, mask_values

TEST_JSON = (
    "  {\n"
    "    \"_id\": \"56af331efbeca6240c61b2ca\",\n"
    "    \"index\": 120000,\n"
    "    \"token\": \"bedb2018-c017-429E-b520-696ea3666692\",\n"
    "    \"access-token\": \"ed0e4b34-13f9-11e9-80f6-80fa5b27636b\",\n"
    "    \"isActive\": false,\n"
    "    \"object\": {\n"
    "		\"token\": \"123am af   asd\"\n"
    "	}\n"
    "}\n\n"
)

TEST_YAML = (
    "_id: 56af331efbeca6240c61b2ca\n"
    "index: 120000\n"
    "token: bedb2018-c017-429E-b520-696ea3666692\n"
    "access-token: ed0e4b34-13f9-11e9-80f6-80fa5b27636b\n"
    "isActive: false\n"
    "object:\n"
    "  token: \"123am af   asd\"\n"
)

simple_test_data = [
    (
        '{ "token"     : "6e0438a8-10bb-11e9-bc54-80fa5b27636b", '
        '"access-token": "ed0e4b34-13f9-11e9-80f6-80fa5b27636b" }',
        lambda source: json.loads(source)
    ),
    (
        'token    : "9b5af948-10ea-11e9-ab67-80fa5b27636b"\n'
        'access-token: "ed0e4b34-13f9-11e9-80f6-80fa5b27636b"',
        lambda source: yaml.safe_load(source)
    ),
]
simple_ids = ["simple-JSON", "simple-YAML"]

complex_test_data = [
    (TEST_JSON, lambda source: json.loads(source)),
    (TEST_YAML, lambda source: yaml.safe_load(source)),
]
complex_ids = ["complex-JSON", "complex-YAML"]

# Simple parametrized test cases handling both JSON and YAML masking


@pytest.mark.parametrize('source', [TEST_JSON, TEST_YAML], ids=complex_ids)
def test_non_existing_key(source):
    """Should leave source unchanged when masking a non-existing key"""
    masked = mask_values(source, ['_not_existing'], 'telepresence')
    assert_that(masked, equal_to(source))


@pytest.mark.parametrize(
    'source,unmarshal', complex_test_data, ids=complex_ids
)
def test_should_mask_multiple_keys(source, unmarshal):
    masked_key = mask_values(source, ['_id', 'token'], 'telepresence')
    as_json = unmarshal(masked_key)
    assert_that(as_json['_id'], equal_to('telepresence'))
    assert_that(as_json['token'], equal_to('telepresence'))
    assert_that(as_json['object']['token'], equal_to('telepresence'))


@pytest.mark.parametrize(
    'source,unmarshal',
    simple_test_data + complex_test_data,
    ids=simple_ids + complex_ids,
)
def test_should_mask_token(source, unmarshal):
    masked_key = mask_sensitive_data(source)
    assert_that(
        unmarshal(masked_key)['token'], equal_to('Masked-by-Telepresence')
    )
    assert_that(
        unmarshal(masked_key)['access-token'],
        equal_to('Masked-by-Telepresence')
    )


# Generated test cases using Hypothesis test engine


@st.composite
def generate_dictionary_with_fixed_tokens(draw):
    """
    Builds random nested dictionary structure which is then used as JSON to
    mask two fixed "token" keys.

    Structure is based on TEST_JSON sample fixture defined above.
    """
    base = draw(
        st.fixed_dictionaries({'token': st.text(printable, min_size=10)})
    )

    optional = draw(
        st.nothing() | st.dictionaries(
            st.text(ascii_letters, min_size=1),
            st.floats() | st.integers() | st.text(printable) | st.booleans()
            | st.nothing(),
            min_size=10,
            max_size=50
        )
    )

    return {**base, **optional}


base_data = st.fixed_dictionaries({
    '_id': st.text(printable),
    'token': st.text(printable),
    'object': generate_dictionary_with_fixed_tokens()
})


@given(base_data)
@settings(max_examples=32)
def test_fuzzy_should_mask_token_keys(fixture):
    example = json.dumps(fixture)
    masked_key = mask_values(example, ['token'], 'telepresence')
    assert_that(json.loads(masked_key)['token'], equal_to('telepresence'))
    assert_that(
        json.loads(masked_key)['object']['token'], equal_to('telepresence')
    )

# Copyright 2019 Datawire. All rights reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

import re
from functools import partial
from typing import Iterable, Match

# See https://regex101.com/r/pHbVhO/2 for usage with "token" keys
KEY_VALUE_REGEX = (
    r'(?:[\"\']*)(?P<key>%s)(?:[\"\']*)(?:\s*)(?=:)(?:\:\s*)'
    r'(?:(?:"(?P<in_double_quotes>'
    r'[^"\\]*(?:\\.[^"\\]*)*'
    r'.*?)")|'
    r'(?:\'(?P<in_single_quotes>.*?)\')|'
    r'(?P<plain_value>(?:[^\n ]*).*?),?)'
)


def _replace_closure(replacement: str, m: Match[str]) -> str:
    def _replace_group(index: int) -> str:
        before = m.span(index)[0] - m.span()[0]
        after = m.span(index)[1] - m.span()[0]
        group = m.group()
        return group[:before] + replacement + group[after:]

    if m.group('in_double_quotes') is not None:
        return _replace_group(2)

    if m.group('in_single_quotes') is not None:
        return _replace_group(3)

    if m.group('plain_value') is not None:
        return _replace_group(4)

    assert False, m


def mask_values(source: str, keys: Iterable[str], mask: str) -> str:
    """
    :param source: string to perform replacement on
    :param keys: list of keys to be matched
    :param mask: string used to mask the value of specified keys
    :return:
    """
    regex = KEY_VALUE_REGEX % "|".join(keys)
    return re.sub(regex, partial(_replace_closure, mask), source)


def mask_sensitive_data(source: str) -> str:
    return mask_values(
        source, ['token', 'access-token'], 'Masked-by-Telepresence'
    )

# Copyright 2018 Datawire. All rights reserved.
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

import atexit
import json
from time import time


class Cache(object):
    """
    Cache JSON-compatible values with string key lookup.

    Usage:

    >>> cache = Cache.load("cache.json")
    >>> cache.invalidate(12 * 60 * 60)  # 12 hours
    >>> sound_cache = cache.child("sound")
    >>> sound_cache.lookup("moo", lambda: "Sound heard near cows")
    'Sound heard near cows'
    >>> sound_cache["moo"]
    'Sound heard near cows'
    >>> number_cache = cache.child("numbers")
    >>> number_cache["one"] = 1
    >>> number_cache["pi"] = 22.0/7.0
    """

    @classmethod
    def load(cls, filename):
        """Return a cache loaded from a file"""
        try:
            with open(filename, "r") as f:
                cache = json.load(f)
        except FileNotFoundError:
            cache = {}

        result = Cache(cache)

        def save():
            """Overwrite the original file with current cache contents"""
            with open(filename, "w") as f:
                json.dump(result.values, f)

        atexit.register(save)
        return result

    def __init__(self, values):
        self.values = values

    def __contains__(self, key):
        return key in self.values

    def __getitem__(self, key):
        return self.values[key]

    def __setitem__(self, key, value):
        self.values[key] = value

    def child(self, key):
        """
        Retrieve a child cache that operates over a separate keyspace but is
        loaded and saved with the parent cache.
        """
        if key in self.values:
            child = self.values[key]
        else:
            child = {}
            self.values[key] = child
        return Cache(child)

    def invalidate(self, ttl):
        """
        Clear the cache if it is too old.

        :param ttl: Time in seconds that the cache is considered valid.
        """
        now = time()
        created = self.lookup("created", lambda: 0)
        if (now - created) > ttl:
            self.clear()
            self["created"] = now

    def lookup(self, key, function):
        """
        Retrieve the value for the associated key. If the value is not already
        cached, call function with no arguments to compute the value and cache
        it for future reference.

        :param key: Identifier for cached value.
        :param function: Thunk used to compute the value if it is not cached.
        :return: The requested value.
        """
        if key in self.values:
            return self.values[key]
        else:
            value = function()
            self.values[key] = value
            return value

    def clear(self):
        """Clear the cache"""
        self.values.clear()

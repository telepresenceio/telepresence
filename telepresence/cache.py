import atexit
import json


class Cache(object):
    @classmethod
    def load(cls, filename):
        try:
            with open(filename, "r") as f:
                cache = json.load(f)
        except FileNotFoundError:
            cache = {}

        result = Cache(cache)

        def flush():
            with open(filename, "w") as f:
                json.dump(result.values, f)

        atexit.register(flush)
        return result

    def __init__(self, values):
        self.values = values

    def __getitem__(self, key):
        return self.values[key]

    def __setitem__(self, key, value):
        self.values[key] = value

    def child(self, key):
        if key in self.values:
            child = self.values[key]
        else:
            child = {}
            self.values[key] = child
        return Cache(child)

    def lookup(self, key, function):
        if key in self.values:
            return self.values[key]
        else:
            value = function()
            self.values[key] = value
            return value

    def clear(self):
        self.values.clear()

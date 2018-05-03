from time import time


class Span(object):
    emit_summary = False

    def __init__(self, runner, tag, parent, verbose=True):
        self.runner = runner
        self.tag = tag
        self.parent = parent
        self.children = []
        if self.parent:
            self.parent.children.append(self)
            self.depth = self.parent.depth + 1
        else:
            self.depth = 0
        self.start_time = None
        self.end_time = None
        self.verbose = verbose

    def begin(self):
        self.start_time = time()
        if self.verbose:
            self.runner.write("BEGIN SPAN {}".format(self.tag))

    def end(self):
        self.end_time = time()
        spent = self.end_time - self.start_time
        if self.runner.current_span == self:
            self.runner.current_span = self.parent
        if self.verbose:
            self.runner.write("END SPAN {} {:6.1f}s".format(self.tag, spent))
        if self.parent is None and Span.emit_summary:
            self.runner.write("SPAN SUMMARY:")
            self.summarize()
        return spent

    def summarize(self):
        indent = self.depth * "  "
        if self.end_time:
            spent = "{:6.1f}s".format(self.end_time - self.start_time)
        else:
            spent = "   ???"
        self.runner.write("{}{} {}".format(spent, indent, self.tag))
        for ch in self.children:
            ch.summarize()

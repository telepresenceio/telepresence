import multiprocessing

class RWLock(object):
    """
    A read-write lock with read-blocks-write semantics.
    """
    def __init__(self):
        self._readlock = multiprocessing.Semaphore()
        self._writelock = multiprocessing.Lock()
        self._counterlock = multiprocessing.Lock()
        self._counter = multiprocessing.Value('i', 0)


    def lock_read(self):
        """
        Acquire a read lock.  As long as it is held, the write lock is also held.
        """
        self._readlock.acquire()
        with self._counterlock:
            self._counter.value += 1
            counter = self._counter.value
        if counter == 1:
            self._writelock.acquire()


    def unlock_read(self):
        """
        Release a read lock.  If no read locks are held anymore, the write lock is
        also released.
        """
        self._readlock.release()
        with self._counterlock:
            self._counter.value -= 1
            counter = self._counter.value
        if counter == 0:
            self._writelock.release()


    def lock_write(self):
        """
        Acquire the write lock (blocking on read locks).
        """
        self._writelock.acquire()


    def unlock_write(self):
        """
        Release the write lock.
        """
        self._writelock.release()

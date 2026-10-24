package vfs

import "sync"

// spool keeps track of the uploads in progress, so that a new upload only
// starts when its whole max_file_size still fits on the disk next to them.
// Without this, a large upload could fail halfway when the disk fills up.
var spool = struct {
	mu     sync.Mutex
	active map[*upload]struct{}
}{active: map[*upload]struct{}{}}

// reserve starts tracking u if the disk has room for it. Room means: free
// space, minus what the uploads already running may still write, minus u's
// own limit, stays above minFree.
//
// An upload with no max_file_size reserves nothing extra, so only minFree
// protects it.
func reserve(u *upload, minFree int64) error {
	spool.mu.Lock()
	defer spool.mu.Unlock()

	free, err := FreeSpace(u.dir)
	if err != nil {
		// The disk can't be measured (unusual); don't block uploads over it.
		spool.active[u] = struct{}{}
		return nil
	}

	needed := u.max
	for other := range spool.active {
		if other.dir == u.dir {
			needed += other.stillToWrite()
		}
	}
	if free-needed < minFree {
		return errSpoolFull
	}
	spool.active[u] = struct{}{}
	return nil
}

// release stops tracking u. It is safe to call more than once.
func release(u *upload) {
	spool.mu.Lock()
	defer spool.mu.Unlock()
	delete(spool.active, u)
}

// stillToWrite is how many more bytes u may add to the disk: its limit
// minus what it has written already.
func (u *upload) stillToWrite() int64 {
	if u.max == 0 {
		return 0
	}
	return max(u.max-u.size.Load(), 0)
}

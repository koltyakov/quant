package lock

type lockFile interface {
	tryLock() error
	unlock() error
	writeInfo(info LockInfo) error
	close() error
	fdInt() int
}

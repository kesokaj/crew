package diskguard

import "syscall"

// Statfs reads the disk that holds dir, as df does.
func Statfs(dir string) (Usage, error) {
	var s syscall.Statfs_t
	if err := syscall.Statfs(dir, &s); err != nil {
		return Usage{}, err
	}
	bs := uint64(s.Bsize)
	return Usage{Used: (s.Blocks - s.Bfree) * bs, Avail: s.Bavail * bs}, nil
}

package agent

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"sync/atomic"

	"golang.org/x/sys/unix"
)

// openBeneath opens a path inside a site root for a root-privileged operation,
// closing the race that a plain check-then-open leaves open.
//
// The problem: resolveInSite validates a path and returns it; the caller then
// opens that path in a separate syscall. The directories in between belong to
// the site's Unix user, so a site compromised through its own application code
// — the exact thing the jail exists to contain — can swap a component for a
// symlink in that window and redirect root's read or write outside the jail.
//
// The fix: resolve the path to its real, symlink-free form and confirm it sits
// beneath the site root (resolveInSite already does this), then open THAT path
// with openat2(RESOLVE_NO_SYMLINKS). The kernel re-checks, atomically, that no
// component is a symlink at the moment of opening. If an attacker swapped one
// in after the check, the open fails rather than escaping.
//
// Note this deliberately does NOT use RESOLVE_BENEATH: that rejects absolute
// symlink targets outright, and Slipstream's release layout depends on them
// (a site's "current" points at an absolute releases/<id> path, and wp-config
// and uploads point into shared/). RESOLVE_BENEATH would make the file manager
// unable to open any real file in a site. Resolving first and forbidding
// symlinks at open time gives the same guarantee for this layout.
//
// Requires Linux 5.6+ (Ubuntu 24.04 ships 6.8). On an older kernel it falls
// back to a plain open, which is what the code did before.
func openBeneath(root, rel string, flags int, perm os.FileMode) (*os.File, error) {
	real, err := realPathInSite(root, rel)
	if err != nil {
		return nil, err
	}

	how := &unix.OpenHow{
		Flags:   uint64(flags) | unix.O_CLOEXEC,
		Mode:    uint64(perm.Perm()),
		Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	}
	fd, err := unix.Openat2(unix.AT_FDCWD, real, how)
	if err != nil {
		if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EPERM) {
			openat2Unavailable.Store(true)
			return os.OpenFile(real, flags, perm)
		}
		if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.EXDEV) {
			// A component became a symlink after we validated the path.
			return nil, fmt.Errorf("path changed while opening; refusing to follow it")
		}
		return nil, err
	}
	return os.NewFile(uintptr(fd), real), nil
}

// realPathInSite returns the fully symlink-resolved absolute path for rel
// inside root, having verified it stays within the site. For a file that does
// not exist yet the parent is resolved and the final name appended.
func realPathInSite(root, rel string) (string, error) {
	full, err := resolveInSite(root, rel)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(full); err == nil {
		return resolved, nil
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(full))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(full)), nil
}

// openat2Unavailable records whether we had to fall back to a plain open, so
// tests can tell strict enforcement from best-effort.
var openat2Unavailable atomic.Bool

// fchownFile sets ownership through an open descriptor. chown(1) follows
// symlinks, so doing it by path lets a swapped component hand a site user
// ownership of an arbitrary root-owned file; a descriptor cannot be redirected.
func fchownFile(f *os.File, uid, gid int) error {
	return unix.Fchown(int(f.Fd()), uid, gid)
}

// siteOwner resolves a site's Unix user to a uid/gid pair for fchown.
func siteOwner(systemUser string) (int, int, error) {
	u, err := user.Lookup(systemUser)
	if err != nil {
		return 0, 0, err
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return 0, 0, err
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return 0, 0, err
	}
	return uid, gid, nil
}

// writeBeneath writes content to a path inside a site root and gives it to the
// site's user, with no symlink followed at open time and ownership applied
// through the descriptor rather than by path.
func writeBeneath(root, rel, systemUser, content string, perm os.FileMode) error {
	f, err := openBeneath(root, rel, unix.O_WRONLY|unix.O_CREAT|unix.O_TRUNC, perm)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		return err
	}
	if uid, gid, err := siteOwner(systemUser); err == nil {
		_ = fchownFile(f, uid, gid)
	}
	return f.Close()
}

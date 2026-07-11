package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/slipstream-panel/slipstream/internal/rpc"
)

// The file browser is deliberately jailed to a site's root. resolveInSite
// is the security boundary: it rejects any path that escapes RootPath after
// cleaning AND after resolving symlinks — a site user could otherwise plant
// a symlink (e.g. wp-config.php -> /etc/shadow) inside its own docroot and
// trick the root agent into reading or writing outside the jail.
func resolveInSite(rootPath, rel string) (string, error) {
	clean := filepath.Clean("/" + rel) // force absolute, collapse .. at root
	full := filepath.Join(rootPath, clean)
	rp := filepath.Clean(rootPath)
	if full != rp && !strings.HasPrefix(full, rp+string(os.PathSeparator)) {
		return "", fmt.Errorf("path escapes site root")
	}

	// Resolve symlinks and re-check containment against the real root. For a
	// path that does not exist yet (a new file being written), resolve its
	// parent directory instead.
	realRoot, err := filepath.EvalSymlinks(rp)
	if err != nil {
		return "", err
	}
	realFull, err := filepath.EvalSymlinks(full)
	if err != nil {
		realParent, perr := filepath.EvalSymlinks(filepath.Dir(full))
		if perr != nil {
			return "", perr
		}
		realFull = filepath.Join(realParent, filepath.Base(full))
	}
	if realFull != realRoot && !strings.HasPrefix(realFull, realRoot+string(os.PathSeparator)) {
		return "", fmt.Errorf("path escapes site root via symlink")
	}
	return full, nil
}

// ListFiles returns a directory listing under a site root.
func (a *Agent) ListFiles(p rpc.ListFilesParams) (rpc.ListFilesResult, error) {
	if err := validateSite(p.Site); err != nil {
		return rpc.ListFilesResult{}, err
	}
	full, err := resolveInSite(p.Site.RootPath, p.RelPath)
	if err != nil {
		return rpc.ListFilesResult{}, err
	}
	entries, err := os.ReadDir(full)
	if err != nil {
		return rpc.ListFilesResult{}, err
	}
	res := rpc.ListFilesResult{RelPath: p.RelPath}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		res.Entries = append(res.Entries, rpc.FileEntry{
			Name:    e.Name(),
			IsDir:   e.IsDir(),
			Size:    info.Size(),
			Mode:    info.Mode().String(),
			ModTime: info.ModTime().UTC().Format(time.RFC3339),
		})
	}
	sort.Slice(res.Entries, func(i, j int) bool {
		if res.Entries[i].IsDir != res.Entries[j].IsDir {
			return res.Entries[i].IsDir
		}
		return res.Entries[i].Name < res.Entries[j].Name
	})
	return res, nil
}

const maxEditableFile = 2 << 20 // 2 MB

// ReadFile returns a text file's content (capped) for the editor.
func (a *Agent) ReadFile(p rpc.ReadFileParams) (rpc.ReadFileResult, error) {
	if err := validateSite(p.Site); err != nil {
		return rpc.ReadFileResult{}, err
	}
	full, err := resolveInSite(p.Site.RootPath, p.RelPath)
	if err != nil {
		return rpc.ReadFileResult{}, err
	}
	fi, err := os.Stat(full)
	if err != nil {
		return rpc.ReadFileResult{}, err
	}
	if fi.IsDir() {
		return rpc.ReadFileResult{}, fmt.Errorf("path is a directory")
	}
	f, err := os.Open(full)
	if err != nil {
		return rpc.ReadFileResult{}, err
	}
	defer f.Close()
	buf := make([]byte, maxEditableFile)
	n, _ := f.Read(buf)
	res := rpc.ReadFileResult{RelPath: p.RelPath, Content: string(buf[:n])}
	if fi.Size() > int64(n) {
		res.Truncated = true
	}
	return res, nil
}

// WriteFile writes text content to a file under the site root, owned by the
// site user.
func (a *Agent) WriteFile(p rpc.WriteFileParams) (map[string]string, error) {
	if err := validateSite(p.Site); err != nil {
		return nil, err
	}
	if len(p.Content) > maxEditableFile {
		return nil, fmt.Errorf("file too large to edit here")
	}
	full, err := resolveInSite(p.Site.RootPath, p.RelPath)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(full, []byte(p.Content), 0o640); err != nil {
		return nil, err
	}
	a.Runner.Run(context.Background(), "chown", p.Site.SystemUser+":"+p.Site.SystemUser, full)
	return map[string]string{"written": p.RelPath}, nil
}

// SetSFTP sets a site user's password so it can authenticate for SFTP.
// Site users keep the nologin shell; the installer's sshd Match block
// grants them an internal-sftp subsystem chrooted to their home, so a
// password is all that is needed and no interactive shell is ever granted.
func (a *Agent) SetSFTP(p rpc.SFTPParams) (map[string]string, error) {
	if !systemUserRe.MatchString(p.SystemUser) {
		return nil, fmt.Errorf("invalid system user")
	}
	ctx := context.Background()
	if !p.Enable {
		a.Runner.Run(ctx, "passwd", "-l", p.SystemUser)
		return map[string]string{"sftp": "disabled"}, nil
	}
	if len(p.Password) < 12 {
		return nil, fmt.Errorf("sftp password too short")
	}
	if err := a.setUnixPassword(ctx, p.SystemUser, p.Password); err != nil {
		return nil, err
	}
	return map[string]string{"sftp": "enabled", "user": p.SystemUser}, nil
}

// setUnixPassword sets a user's password via chpasswd, feeding the value on
// stdin so it never appears in argv or the process list.
func (a *Agent) setUnixPassword(ctx context.Context, user, password string) error {
	cmd := exec.CommandContext(ctx, "chpasswd")
	cmd.Stdin = strings.NewReader(user + ":" + password + "\n")
	var errBuf strings.Builder
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("set password: %w: %s", err, strings.TrimSpace(errBuf.String()))
	}
	return nil
}

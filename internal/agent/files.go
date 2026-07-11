package agent

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
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

const maxEditableFile = 2 << 20  // 2 MB
const maxTransferFile = 16 << 20 // 16 MB; use SFTP for larger transfers

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

// TransferFile moves bounded binary data through the authenticated RPC
// channel. Larger transfers belong on SFTP so the panel remains memory-bound.
func (a *Agent) TransferFile(p rpc.TransferFileParams) (rpc.TransferFileResult, error) {
	if err := validateSite(p.Site); err != nil {
		return rpc.TransferFileResult{}, err
	}
	if err := a.validateSiteRoot(p.Site); err != nil {
		return rpc.TransferFileResult{}, err
	}
	if strings.TrimSpace(p.RelPath) == "" {
		return rpc.TransferFileResult{}, fmt.Errorf("file path required")
	}
	full, err := resolveInSite(p.Site.RootPath, p.RelPath)
	if err != nil {
		return rpc.TransferFileResult{}, err
	}
	if p.Upload {
		if len(p.Data) > maxTransferFile {
			return rpc.TransferFileResult{}, fmt.Errorf("upload exceeds 16 MB; use SFTP for larger files")
		}
		if fi, err := os.Stat(full); err == nil && fi.IsDir() {
			return rpc.TransferFileResult{}, fmt.Errorf("destination is a directory")
		}
		tmp, err := os.CreateTemp(filepath.Dir(full), ".slipstream-upload-*")
		if err != nil {
			return rpc.TransferFileResult{}, err
		}
		tmpName := tmp.Name()
		defer os.Remove(tmpName)
		if _, err := tmp.Write(p.Data); err != nil {
			tmp.Close()
			return rpc.TransferFileResult{}, err
		}
		if err := tmp.Chmod(0o640); err != nil {
			tmp.Close()
			return rpc.TransferFileResult{}, err
		}
		if err := tmp.Close(); err != nil {
			return rpc.TransferFileResult{}, err
		}
		if err := os.Rename(tmpName, full); err != nil {
			return rpc.TransferFileResult{}, err
		}
		a.Runner.Run(context.Background(), "chown", p.Site.SystemUser+":"+p.Site.SystemUser, full)
		return rpc.TransferFileResult{Name: filepath.Base(full), Size: int64(len(p.Data))}, nil
	}
	fi, err := os.Stat(full)
	if err != nil {
		return rpc.TransferFileResult{}, err
	}
	if !fi.Mode().IsRegular() {
		return rpc.TransferFileResult{}, fmt.Errorf("path is not a regular file")
	}
	if fi.Size() > maxTransferFile {
		return rpc.TransferFileResult{}, fmt.Errorf("download exceeds 16 MB; use SFTP for larger files")
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return rpc.TransferFileResult{}, err
	}
	return rpc.TransferFileResult{Name: filepath.Base(full), Data: data, Size: fi.Size()}, nil
}

// ManageFile performs structural operations without invoking a shell.
func (a *Agent) ManageFile(p rpc.ManageFileParams) (map[string]string, error) {
	if err := validateSite(p.Site); err != nil {
		return nil, err
	}
	if err := a.validateSiteRoot(p.Site); err != nil {
		return nil, err
	}
	if strings.TrimSpace(p.RelPath) == "" {
		return nil, fmt.Errorf("site root cannot be modified")
	}
	full, err := resolveInSite(p.Site.RootPath, p.RelPath)
	if err != nil {
		return nil, err
	}
	switch p.Operation {
	case "mkdir":
		if err := os.Mkdir(full, 0o750); err != nil {
			return nil, err
		}
		a.Runner.Run(context.Background(), "chown", p.Site.SystemUser+":"+p.Site.SystemUser, full)
		return map[string]string{"created": p.RelPath}, nil
	case "rename":
		if strings.TrimSpace(p.DestPath) == "" {
			return nil, fmt.Errorf("destination path required")
		}
		dest, err := resolveInSite(p.Site.RootPath, p.DestPath)
		if err != nil {
			return nil, err
		}
		if _, err := os.Lstat(dest); err == nil {
			return nil, fmt.Errorf("destination already exists")
		} else if !os.IsNotExist(err) {
			return nil, err
		}
		if err := os.Rename(full, dest); err != nil {
			return nil, err
		}
		return map[string]string{"renamed": p.DestPath}, nil
	case "delete":
		if err := os.RemoveAll(full); err != nil {
			return nil, err
		}
		return map[string]string{"deleted": p.RelPath}, nil
	default:
		return nil, fmt.Errorf("unsupported file operation %q", p.Operation)
	}
}

// SetSFTP sets a site user's password so it can authenticate for SFTP.
// Site users keep the nologin shell; the installer's sshd Match block
// grants them an internal-sftp subsystem chrooted to their home, so a
// password is all that is needed and no interactive shell is ever granted.
func (a *Agent) SetSFTP(p rpc.SFTPParams) (map[string]string, error) {
	if err := validateSite(p.Site); err != nil {
		return nil, err
	}
	if err := a.validateSiteRoot(p.Site); err != nil {
		return nil, err
	}
	ctx := context.Background()
	if !p.Enable {
		a.Runner.Run(ctx, "passwd", "-l", p.Site.SystemUser)
		return map[string]string{"sftp": "disabled"}, nil
	}
	if len(p.Password) < 12 {
		return nil, fmt.Errorf("sftp password too short")
	}
	// Repair older installations whose site root predates the root-owned
	// chroot invariant. This does not alter writable child ownership.
	if _, err := a.Runner.Run(ctx, "chown", "root:"+p.Site.SystemUser, p.Site.RootPath); err != nil {
		return nil, fmt.Errorf("secure SFTP chroot: %w", err)
	}
	if _, err := a.Runner.Run(ctx, "chmod", "750", p.Site.RootPath); err != nil {
		return nil, fmt.Errorf("secure SFTP chroot: %w", err)
	}
	if err := a.setUnixPassword(ctx, p.Site.SystemUser, p.Password); err != nil {
		return nil, err
	}
	return map[string]string{"sftp": "enabled", "user": p.Site.SystemUser}, nil
}

var allowedSSHKeyTypes = map[string]bool{
	"ssh-ed25519": true, "ssh-rsa": true,
	"ecdsa-sha2-nistp256": true, "ecdsa-sha2-nistp384": true, "ecdsa-sha2-nistp521": true,
}

func parseSSHKey(line string) (rpc.SSHKey, string, error) {
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) < 2 || !allowedSSHKeyTypes[fields[0]] {
		return rpc.SSHKey{}, "", fmt.Errorf("unsupported or malformed SSH public key")
	}
	blob, err := base64.StdEncoding.DecodeString(fields[1])
	if err != nil || len(blob) < 32 || len(blob) > 16<<10 {
		return rpc.SSHKey{}, "", fmt.Errorf("malformed SSH public key data")
	}
	sum := sha256.Sum256(blob)
	fingerprint := "SHA256:" + base64.RawURLEncoding.EncodeToString(sum[:])
	label := ""
	if len(fields) > 2 {
		label = strings.Join(fields[2:], " ")
		if len(label) > 120 {
			label = label[:120]
		}
	}
	normalized := fields[0] + " " + fields[1]
	if label != "" {
		normalized += " " + label
	}
	return rpc.SSHKey{Type: fields[0], Fingerprint: fingerprint, Label: label}, normalized, nil
}

// SSHKeys manages public-key authentication for the same chrooted,
// ForceCommand-restricted SFTP identity. Keys never grant an interactive shell.
func (a *Agent) SSHKeys(p rpc.SSHKeyParams) (rpc.SSHKeysResult, error) {
	if err := validateSite(p.Site); err != nil {
		return rpc.SSHKeysResult{}, err
	}
	if err := a.validateSiteRoot(p.Site); err != nil {
		return rpc.SSHKeysResult{}, err
	}
	sshDir := filepath.Join(p.Site.RootPath, ".ssh")
	keyFile := filepath.Join(sshDir, "authorized_keys")
	read := func() ([]string, error) {
		b, err := os.ReadFile(keyFile)
		if os.IsNotExist(err) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		var lines []string
		for _, line := range strings.Split(string(b), "\n") {
			if strings.TrimSpace(line) != "" {
				lines = append(lines, line)
			}
		}
		return lines, nil
	}
	lines, err := read()
	if err != nil {
		return rpc.SSHKeysResult{}, err
	}
	if p.Action == "list" {
		res := rpc.SSHKeysResult{Keys: []rpc.SSHKey{}}
		for _, line := range lines {
			if key, _, err := parseSSHKey(line); err == nil {
				res.Keys = append(res.Keys, key)
			}
		}
		return res, nil
	}
	if _, err := a.Runner.Run(context.Background(), "chown", "root:"+p.Site.SystemUser, p.Site.RootPath); err != nil {
		return rpc.SSHKeysResult{}, fmt.Errorf("secure SFTP chroot: %w", err)
	}
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		return rpc.SSHKeysResult{}, err
	}
	if p.Action == "add" {
		key, normalized, err := parseSSHKey(p.PublicKey)
		if err != nil {
			return rpc.SSHKeysResult{}, err
		}
		for _, line := range lines {
			existing, _, err := parseSSHKey(line)
			if err == nil && existing.Fingerprint == key.Fingerprint {
				return rpc.SSHKeysResult{}, fmt.Errorf("SSH key already exists")
			}
		}
		lines = append(lines, normalized)
		if status, err := a.Runner.Run(context.Background(), "passwd", "-S", p.Site.SystemUser); err == nil {
			fields := strings.Fields(status)
			if len(fields) > 1 && fields[1] == "L" {
				// An unlocked account is required for public-key auth. An empty
				// password remains unusable because sshd forbids empty passwords.
				a.Runner.Run(context.Background(), "passwd", "-d", p.Site.SystemUser)
			}
		}
	} else if p.Action == "delete" {
		kept := lines[:0]
		found := false
		for _, line := range lines {
			key, _, err := parseSSHKey(line)
			if err == nil && key.Fingerprint == p.Fingerprint {
				found = true
				continue
			}
			kept = append(kept, line)
		}
		if !found {
			return rpc.SSHKeysResult{}, fmt.Errorf("SSH key not found")
		}
		lines = kept
	} else {
		return rpc.SSHKeysResult{}, fmt.Errorf("unsupported SSH key action %q", p.Action)
	}
	content := ""
	if len(lines) > 0 {
		content = strings.Join(lines, "\n") + "\n"
	}
	if err := os.WriteFile(keyFile, []byte(content), 0o600); err != nil {
		return rpc.SSHKeysResult{}, err
	}
	if err := os.Chmod(keyFile, 0o600); err != nil {
		return rpc.SSHKeysResult{}, err
	}
	a.Runner.Run(context.Background(), "chown", "-R", p.Site.SystemUser+":"+p.Site.SystemUser, sshDir)
	if len(lines) == 0 && !p.Site.Config.SFTPEnabled {
		a.Runner.Run(context.Background(), "passwd", "-l", p.Site.SystemUser)
	}
	return a.SSHKeys(rpc.SSHKeyParams{Site: p.Site, Action: "list"})
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

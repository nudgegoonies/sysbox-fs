//
// Copyright: (C) 2020 Nestybox Inc.  All rights reserved.
//

package process

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"github.com/nestybox/sysbox-fs/domain"
	cap "github.com/nestybox/sysbox-fs/process/capability"

	"github.com/sirupsen/logrus"
	"github.com/spf13/afero"
	"golang.org/x/sys/unix"
)

var AppFs = afero.NewOsFs()

type processService struct{}

func NewProcessService() domain.ProcessService {
	return &processService{}
}

func (ps *processService) ProcessCreate(
	pid uint32,
	uid uint32,
	gid uint32) domain.ProcessIface {

	return &process{
		pid: pid,
		uid: uid,
		gid: gid,
	}
}

type process struct {
	pid      uint32                  // process id
	root     string                  // root dir
	cwd      string                  // current working dir
	uid      uint32                  // effective uid
	gid      uint32                  // effective gid
	sgid     []int                   // supplementary groups
	cap      cap.Capabilities        // process capabilities
	status   map[string]string       // process status fields
	nsInodes map[string]domain.Inode // process namespace inodes
}

//
// Public methods
//

func (p *process) Pid() uint32 {
	return p.pid
}

func (p *process) Uid() uint32 {
	return p.uid
}

func (p *process) Gid() uint32 {
	return p.gid
}

// Simple wrapper method to set capability values.
func (p *process) SetCapability(which domain.CapType, what ...domain.Cap) {

	if p.cap == nil {
		if err := p.initCapability(); err != nil {
			return
		}
	}

	for _, elem := range what {
		p.cap.Set(which, elem)
	}
}

// Simple wrapper method to determine capability presence.
func (p *process) IsCapabilitySet(which domain.CapType, what domain.Cap) bool {

	if p.cap == nil {
		if err := p.initCapability(); err != nil {
			return false
		}
	}

	return p.cap.Get(which, what)
}

// initCapability method retrieves process capabilities from kernel and store
// them within 'capability' data-struct.
func (p *process) initCapability() error {

	c, err := cap.NewPid2(int(p.pid))
	if err != nil {
		return err
	}

	if err = c.Load(); err != nil {
		return err
	}

	p.cap = c

	return nil
}

func (p *process) NsInodes() (map[string]domain.Inode, error) {

	// First invocation causes the process ns inodes to be parsed
	if p.nsInodes == nil {
		nsInodes, err := getNsInodes(p.pid)
		if err != nil {
			return nil, err
		}
		p.nsInodes = nsInodes
	}

	return p.nsInodes, nil
}

func (p *process) UserNsInode() domain.Inode {
	nsInodes, err := p.NsInodes()
	if err != nil {
		return domain.Inode(0)
	}
	userns, found := nsInodes["user"]
	if !found {
		return domain.Inode(0)
	}
	return userns
}

func (p *process) UserNsInodeParent() (domain.Inode, error) {

	// ioctl to retrieve the parent namespace.
	const NS_GET_PARENT = 0xb702

	usernsPath := filepath.Join("/proc", strconv.FormatUint(uint64(p.pid), 10), "ns", "user")

	// Open /proc/<pid>/ns/user to obtain a file-desc to refer to.
	childNsFd, err := os.Open(usernsPath)
	if err != nil {
		return 0, err
	}
	defer childNsFd.Close()

	// Launch ioctl to collect parent namespace fd.
	ret, _, errno := unix.Syscall(
		unix.SYS_IOCTL,
		childNsFd.Fd(),
		uintptr(NS_GET_PARENT),
		0)
	if errno != 0 {
		return 0, errno
	}
	parentNsFd := (int)((uintptr)(unsafe.Pointer(ret)))
	defer syscall.Close(parentNsFd)

	// Run stat() over the returned file descriptor to obtain the inode that
	// uniquely identifies the parent namespace.
	var stat syscall.Stat_t
	err = syscall.Fstat(parentNsFd, &stat)
	if err != nil {
		return 0, err
	}

	return stat.Ino, nil
}

// PathAccess emulates the path resolution and permission checking process done by
// the Linux kernel, as described in path_resolution(7).
//
// It checks if the process with the given pid can access the file or directory at the
// given path. The given mode determines what type of access to check for (e.g., read,
// write, execute, or a combination of these).
//
// The given path may be absolute or relative. Each component of the path is checked to
// see if it exists and whether the process has permissions to access it, following the
// rules for path resolution in Linux (see path_resolution(7)). The path may contain ".",
// "..", and symlinks. For absolute paths, the check is done starting from the process'
// root directory. For relative paths, the check is done starting from the process'
// current working directory.
//
// Returns nil if the process can access the path, or one of the following errors
// otherwise:
//
// syscall.ENOENT: some component of the path does not exist.
// syscall.ENOTDIR: a non-final component of the path is not a directory.
// syscall.EACCES: the process does not have permission to access at least one component of the path.
// syscall.ELOOP: the path too many symlinks (e.g. > 40).

func (p *process) PathAccess(path string, aMode domain.AccessMode) error {

	if err := p.getInfo(); err != nil {
		return err
	}

	return p.pathAccess(path, aMode)
}

// getInfo retrieves info about the process
func (p *process) getInfo() error {

	space := regexp.MustCompile(`\s+`)

	fields := []string{"Uid", "Gid", "Groups"}
	if err := p.getStatus(fields); err != nil {
		return err
	}

	// effective uid
	str := space.ReplaceAllString(p.status["Uid"], " ")
	str = strings.TrimSpace(str)
	uids := strings.Split(str, " ")
	if len(uids) != 4 {
		return fmt.Errorf("invalid uid status: %+v", uids)
	}
	euid, err := strconv.Atoi(uids[1])
	if err != nil {
		return err
	}

	// effective gid
	str = space.ReplaceAllString(p.status["Gid"], " ")
	str = strings.TrimSpace(str)
	gids := strings.Split(str, " ")
	if len(gids) != 4 {
		return fmt.Errorf("invalid gid status: %+v", gids)
	}
	egid, err := strconv.Atoi(gids[1])
	if err != nil {
		return err
	}

	// supplementary groups
	sgid := []int{}
	str = space.ReplaceAllString(p.status["Groups"], " ")
	str = strings.TrimSpace(str)
	groups := strings.Split(str, " ")
	for _, g := range groups {
		if g == "" {
			continue
		}
		val, err := strconv.Atoi(g)
		if err != nil {
			return err
		}
		sgid = append(sgid, val)
	}

	// process root & cwd
	root := fmt.Sprintf("/proc/%d/root", p.pid)
	cwd := fmt.Sprintf("/proc/%d/cwd", p.pid)

	// process capabilities
	if p.cap == nil {
		if err := p.initCapability(); err != nil {
			return err
		}
	}

	// store all collected attributes
	p.root = root
	p.cwd = cwd
	p.uid = uint32(euid)
	p.gid = uint32(egid)
	p.sgid = sgid

	return nil
}

// getStatus retrieves process status info obtained from the
// /proc/[pid]/status file.
func (p *process) getStatus(fields []string) error {

	filename := fmt.Sprintf("/proc/%d/status", p.pid)
	f, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer f.Close()

	s := bufio.NewScanner(f)

	status := make(map[string]string)
	for s.Scan() {
		text := s.Text()
		parts := strings.Split(text, ":")

		if len(parts) < 1 {
			continue
		}

		for _, f := range fields {
			if parts[0] == f {
				if len(parts) > 1 {
					status[f] = parts[1]
				} else {
					status[f] = ""
				}
			}
		}
	}

	if err := s.Err(); err != nil {
		return err
	}

	p.status = status

	return nil
}

func (p *process) pathAccess(path string, mode domain.AccessMode) error {

	if path == "" {
		return syscall.ENOENT
	}

	if len(path)+1 > syscall.PathMax {
		return syscall.ENAMETOOLONG
	}

	// Determine the start point.
	var start string
	if filepath.IsAbs(path) {
		start = p.root
	} else {
		start = p.cwd
	}

	// Break up path into it's components; note that repeated "/" results in
	// empty path components.
	components := strings.Split(path, "/")

	cur := start
	linkCnt := 0
	final := false

	for i, c := range components {
		if i == len(components)-1 {
			final = true
		}

		if c == "" {
			continue
		}

		if c == ".." {
			parent := filepath.Dir(cur)
			if !strings.HasPrefix(parent, p.root) {
				parent = p.root
			}
			cur = parent
		} else if c != "." {
			cur = filepath.Join(cur, c)
		}

		symlink, isDir, err := isSymlink(cur)
		if err != nil {
			return syscall.ENOENT
		}

		if !final && !symlink && !isDir {
			return syscall.ENOTDIR
		}

		// Follow the symlink (unless it's the proc.root); may recurse if
		// symlink points to another symlink and so on; we stop at symlinkMax
		// recursions (just as the Linux kernel does).

		if symlink && cur != p.root {
			for {
				if linkCnt >= domain.SymlinkMax {
					return syscall.ELOOP
				}

				link, err := os.Readlink(cur)
				if err != nil {
					return syscall.ENOENT
				}

				if filepath.IsAbs(link) {
					cur = filepath.Join(p.root, link)
				} else {
					cur = filepath.Join(filepath.Dir(cur), link)
				}

				// If 'cur' ever matches 'p.root' then there's no need to continue
				// iterating as we know for sure that 'p.root' is a valid /
				// non-cyclical path. If we were to continue our iteration, we
				// would end up dereferencing 'p.root' -- through readlink() --
				// which would erroneously points us to "/" in the host fs.
				if cur == p.root {
					break
				}

				symlink, isDir, err = isSymlink(cur)
				if err != nil {
					return syscall.ENOENT
				}

				if !symlink {
					break
				}
				linkCnt += 1
			}

			if !final && !isDir {
				return syscall.ENOTDIR
			}
		}

		perm := false
		if !final {
			perm, err = p.checkPerm(cur, domain.X_OK)
		} else {
			perm, err = p.checkPerm(cur, mode)
		}

		if err != nil || !perm {
			return syscall.EACCES
		}
	}

	return nil
}

// checkPerm checks if the given process has permission to access the file or
// directory at the given path. The access mode indicates what type of access is
// being checked (i.e., read, write, execute, or a combination of these). The
// given path must not be a symlink. Returns true if the given process has the
// required permission, false otherwise. The returned error indicates if an
// error occurred during the check.
func (p *process) checkPerm(path string, aMode domain.AccessMode) (bool, error) {

	fi, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	fperm := fi.Mode().Perm()

	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return false, fmt.Errorf("failed to convert to syscall.Stat_t")
	}
	fuid := st.Uid
	fgid := st.Gid

	mode := uint32(aMode)

	// Note: the order of the checks below mimics those done by the Linux kernel.

	// owner check
	if fuid == p.uid {
		perm := uint32((fperm & 0700) >> 6)
		if mode&perm == mode {
			return true, nil
		}
	}

	// group check
	if fgid == p.gid || intSliceContains(p.sgid, fgid) {
		perm := uint32((fperm & 0070) >> 3)
		if mode&perm == mode {
			return true, nil
		}
	}

	// "other" check
	perm := uint32(fperm & 0007)
	if mode&perm == mode {
		return true, nil
	}

	// capability checks
	if p.IsCapabilitySet(domain.EFFECTIVE, domain.CAP_DAC_OVERRIDE) {
		// Per capabilities(7): CAP_DAC_OVERRIDE bypasses file read, write,
		// and execute permission checks.
		//
		// Per The Linux Programming Interface, 15.4.3: A process with the
		// CAP_DAC_OVERRIDE capability always has read and write permissions
		// for any type of file, and also has execute permission if the file
		// is a directory or if execute permission is granted to at least one
		// of the permission categories for the file.
		if fi.IsDir() {
			return true, nil
		} else {
			if aMode&domain.X_OK != domain.X_OK {
				return true, nil
			} else {
				if fperm&0111 != 0 {
					return true, nil
				}
			}
		}
	}

	if p.IsCapabilitySet(domain.EFFECTIVE, domain.CAP_DAC_READ_SEARCH) {
		// Per capabilities(7): CAP_DAC_READ_SEARCH bypasses file read permission
		// checks and directory read and execute permission checks
		if fi.IsDir() && (aMode&domain.W_OK != domain.W_OK) {
			return true, nil
		}

		if !fi.IsDir() && (aMode == domain.R_OK) {
			return true, nil
		}
	}

	return false, nil
}

//
// Miscellaneous auxiliary functions
//

// isSymlink returns true if the given file is a symlink
func isSymlink(path string) (bool, bool, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return false, false, err
	}

	return fi.Mode()&os.ModeSymlink == os.ModeSymlink, fi.IsDir(), nil
}

// intSliceContains returns true if x is in a
func intSliceContains(a []int, x uint32) bool {
	for _, n := range a {
		if int(x) == n {
			return true
		}
	}
	return false
}

// Collects the namespace inodes of the given process
func getNsInodes(pid uint32) (map[string]domain.Inode, error) {

	nsInodes := make(map[string]domain.Inode)
	pidStr := strconv.FormatUint(uint64(pid), 10)

	// Iterate through all namespaces to collect the process' namespace inodes.
	for _, ns := range domain.AllNSs {
		nsPath := filepath.Join("/proc", pidStr, "ns", ns)

		// In unit-testing scenarios we will extract the nsInode value from the
		// file content itself. This is a direct consequence of afero-fs lacking
		// Sys() api support.
		_, ok := AppFs.(*afero.MemMapFs)
		if ok {
			content, err := afero.ReadFile(AppFs, nsPath)
			if err != nil {
				return nil, err
			}
			nsInode, err := strconv.ParseUint(string(content), 10, 64)
			if err != nil {
				return nil, err
			}
			nsInodes[ns] = nsInode
			continue
		}

		info, err := os.Stat(nsPath)
		if err != nil {
			logrus.Errorf("No process %s-ns file found for pid %d", ns, pid)
			return nil, err
		}

		stat := info.Sys().(*syscall.Stat_t)
		nsInodes[ns] = stat.Ino
	}

	return nsInodes, nil
}

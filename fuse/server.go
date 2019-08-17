package fuse

import (
	"errors"
	"os"
	"syscall"

	"bazil.org/fuse"
	"bazil.org/fuse/fs"
	_ "bazil.org/fuse/fs/fstestutil"
	"github.com/spf13/afero"
	"github.com/sirupsen/logrus"

	"github.com/nestybox/sysvisor-fs/domain"
	"github.com/nestybox/sysvisor-fs/sysio"
)

type fuseService struct {
	path       string
	mountPoint string
	server     *fs.Server
	root       *Dir
	ios        domain.IOService
	hds        domain.HandlerService
}

//
// NewFuseService serves as sysvisor-fs' fuse-server constructor.
//
func NewFuseService(
	path string,
	mountPoint string,
	ios domain.IOService,
	hds domain.HandlerService) domain.FuseService {

	// Verify the existence of the requested path in the host FS.
	pathIOnode := ios.NewIOnode(path, path, os.ModeDir)
	pathInfo, err := pathIOnode.Stat()
	if err != nil {
		logrus.Fatal("File-System path not found: ", path)
		return nil
	}

	// Verify the existence of the requested mountpoint in the host FS.
	mountPointIOnode := ios.NewIOnode(mountPoint, mountPoint, os.ModeDir)
	_, err = mountPointIOnode.Stat()
	if err != nil {
		logrus.Fatal("File-System mountpoint not found: ", mountPoint)
		return nil
	}

	// Creating a first node corresponding to the root element in
	// sysvisorfs.
	var attr fuse.Attr
	_, ok := sysio.AppFs.(*afero.OsFs)
	if ok {
		attr = statToAttr(pathInfo.Sys().(*syscall.Stat_t))
	} else {
		attr = fuse.Attr{}
	}

	attr.Mode = os.ModeDir | os.FileMode(int(0777))

	newfs := &fuseService{
		path:       path,
		mountPoint: mountPoint,
		server:     nil,
		ios:        ios,
		hds:        hds,
		root:       nil,
	}

	// Build sysvisor-fs top-most directory (root).
	newfs.root = NewDir(path, path, &attr, newfs)

	return newfs
}

func (s *fuseService) Run() error {
	//
	// Creating a FUSE mount at the requested mountpoint. Notice that we are
	// making use of "allowOther" flag to allow unpriviliged users to access
	// this mount.
	//
	c, err := fuse.Mount(
		s.mountPoint,
		fuse.FSName("sysvisorfs"),
		fuse.AllowOther())
	if err != nil {
		logrus.Fatal(err)
		return err
	}
	defer c.Close()
	if p := c.Protocol(); !p.HasInvalidate() {
		logrus.Fatal("Kernel FUSE support is too old to have invalidations: version %v", p)
		return err
	}

	// Creating a FUSE server to drive kernel interactions.
	s.server = fs.New(c, nil)
	if s.server == nil {
		logrus.Fatal("FUSE file-system could not be created")
		return errors.New("FUSE file-system could not be created")
	}

	logrus.Info("Starting to serve sysvisor-fs...")
	if err := s.server.Serve(s); err != nil {
		logrus.Fatal(err)
		return err
	}

	// Return if any error is reported by mount logic.
	<-c.Ready
	if err := c.MountError; err != nil {
		logrus.Fatal(err)
		return err
	}

	return nil
}

//
// Root method. This is a Bazil-FUSE-lib requirement. Function returns
// sysvisor-fs' root-node.
//
func (s *fuseService) Root() (fs.Node, error) {

	return s.root, nil
}

func (s *fuseService) MountPoint() string {

	return s.mountPoint
}

func (s *fuseService) Unmount() {

	fuse.Unmount(s.mountPoint)
}

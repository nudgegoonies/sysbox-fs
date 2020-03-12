//
// Copyright: (C) 2019 Nestybox Inc.  All rights reserved.
//

package implementations

import (
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"

	"github.com/sirupsen/logrus"

	"github.com/nestybox/sysbox-fs/domain"
	"github.com/nestybox/sysbox-fs/fuse"
)

//
// /proc/cgroups Handler
//
type ProcDiskstatsHandler struct {
	Name      string
	Path      string
	Type      domain.HandlerType
	Enabled   bool
	Cacheable bool
	Service   domain.HandlerService
}

func (h *ProcDiskstatsHandler) Lookup(
	n domain.IOnode,
	req *domain.HandlerRequest) (os.FileInfo, error) {

	logrus.Debugf("Executing Lookup() method on %v handler", h.Name)

	return n.Stat()
}

func (h *ProcDiskstatsHandler) Getattr(
	n domain.IOnode,
	req *domain.HandlerRequest) (*syscall.Stat_t, error) {

	logrus.Debugf("Executing Getattr() method on %v handler", h.Name)

	// Identify the userNsInode corresponding to this pid.
	usernsInode := h.Service.FindUserNsInode(req.Pid)
	if usernsInode == 0 {
		return nil, errors.New("Could not identify userNsInode")
	}

	// If userNsInode matches the one of system's true-root, then return here
	// with UID/GID = 0. This step is required during container initialization
	// phase.
	if usernsInode == h.Service.HostUserNsInode() {
		stat := &syscall.Stat_t{
			Uid: 0,
			Gid: 0,
		}

		return stat, nil
	}

	// Let's refer to the common handler for the rest.
	commonHandler, ok := h.Service.FindHandler("commonHandler")
	if !ok {
		return nil, fmt.Errorf("No commonHandler found")
	}

	return commonHandler.Getattr(n, req)
}

func (h *ProcDiskstatsHandler) Open(
	n domain.IOnode,
	req *domain.HandlerRequest) error {

	logrus.Debugf("Executing %v Open() method", h.Name)

	flags := n.OpenFlags()
	if flags != syscall.O_RDONLY {
		return fuse.IOerror{Code: syscall.EACCES}
	}

	if err := n.Open(); err != nil {
		logrus.Debug("Error opening file ", h.Path)
		return fuse.IOerror{Code: syscall.EIO}
	}

	return nil
}

func (h *ProcDiskstatsHandler) Close(n domain.IOnode) error {

	logrus.Debugf("Executing Close() method on %v handler", h.Name)

	if err := n.Close(); err != nil {
		logrus.Debug("Error closing file ", h.Path)
		return fuse.IOerror{Code: syscall.EIO}
	}

	return nil
}

func (h *ProcDiskstatsHandler) Read(
	n domain.IOnode,
	req *domain.HandlerRequest) (int, error) {

	logrus.Debugf("Executing %v Read() method", h.Name)

	// Bypass emulation logic for now by going straight to host fs.
	ios := h.Service.IOService()
	len, err := ios.ReadNode(n, req.Data)
	if err != nil && err != io.EOF {
		return 0, err
	}

	req.Data = req.Data[:len]

	return len, nil
}

func (h *ProcDiskstatsHandler) Write(
	n domain.IOnode,
	req *domain.HandlerRequest) (int, error) {

	logrus.Debugf("Executing %v Write() method", h.Name)

	return 0, nil
}

func (h *ProcDiskstatsHandler) ReadDirAll(
	n domain.IOnode,
	req *domain.HandlerRequest) ([]os.FileInfo, error) {

	return nil, nil
}

func (h *ProcDiskstatsHandler) GetName() string {
	return h.Name
}

func (h *ProcDiskstatsHandler) GetPath() string {
	return h.Path
}

func (h *ProcDiskstatsHandler) GetEnabled() bool {
	return h.Enabled
}

func (h *ProcDiskstatsHandler) GetType() domain.HandlerType {
	return h.Type
}

func (h *ProcDiskstatsHandler) GetService() domain.HandlerService {
	return h.Service
}

func (h *ProcDiskstatsHandler) SetEnabled(val bool) {
	h.Enabled = val
}

func (h *ProcDiskstatsHandler) SetService(hs domain.HandlerService) {
	h.Service = hs
}

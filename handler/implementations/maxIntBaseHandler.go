//
// Copyright 2019-2020 Nestybox, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//    https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//

package implementations

import (
	"errors"
	"io"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/nestybox/sysbox-fs/domain"
	"github.com/nestybox/sysbox-fs/fuse"
)

// This is a base handler for kernel sysctls exposed inside a sys container that
// consist of a single integer value and where the value written to the host
// kernel is the max value across sys containers.

type MaxIntBaseHandler struct {
	domain.HandlerBase
}

func (h *MaxIntBaseHandler) Lookup(
	n domain.IOnodeIface,
	req *domain.HandlerRequest) (os.FileInfo, error) {

	logrus.Debugf("Executing Lookup() method on %v handler", h.Name)

	return n.Stat()
}

func (h *MaxIntBaseHandler) Getattr(
	n domain.IOnodeIface,
	req *domain.HandlerRequest) (*syscall.Stat_t, error) {

	logrus.Debugf("Executing Getattr() method on %v handler", h.Name)

	return nil, nil
}

func (h *MaxIntBaseHandler) Open(
	n domain.IOnodeIface,
	req *domain.HandlerRequest) error {

	logrus.Debugf("Executing %v Open() method\n", h.Name)

	flags := n.OpenFlags()
	if flags != syscall.O_RDONLY && flags != syscall.O_WRONLY {
		return fuse.IOerror{Code: syscall.EACCES}
	}

	// During 'writeOnly' accesses, we must grant read-write rights temporarily
	// to allow push() to carry out the expected 'write' operation, as well as a
	// 'read' one too.
	if flags == syscall.O_WRONLY {
		n.SetOpenFlags(syscall.O_RDWR)
	}

	if err := n.Open(); err != nil {
		logrus.Debugf("Error opening file %v", h.Path)
		return fuse.IOerror{Code: syscall.EIO}
	}

	return nil
}

func (h *MaxIntBaseHandler) Close(n domain.IOnodeIface) error {

	logrus.Debugf("Executing Close() method on %v handler", h.Name)

	if err := n.Close(); err != nil {
		logrus.Debugf("Error closing file %v", h.Path)
		return fuse.IOerror{Code: syscall.EIO}
	}

	return nil
}

func (h *MaxIntBaseHandler) Read(
	n domain.IOnodeIface,
	req *domain.HandlerRequest) (int, error) {

	var err error

	logrus.Debugf("Executing %v Read() method", h.Name)

	// We are dealing with a single integer element being read, so we can save
	// some cycles by returning right away if offset is any higher than zero.
	if req.Offset > 0 {
		return 0, io.EOF
	}

	name := n.Name()
	path := n.Path()
	cntr := req.Container

	// Ensure operation is generated from within a registered sys container.
	if cntr == nil {
		logrus.Errorf("Could not find the container originating this request (pid %v)",
			req.Pid)
		return 0, errors.New("Container not found")
	}

	// Check if this resource has been initialized for this container. Otherwise,
	// fetch the information from the host FS and store it accordingly within
	// the container struct.
	cntr.Lock()
	data, ok := cntr.Data(path, name)
	if !ok {
		data, err = h.fetchFile(n, cntr)
		if err != nil && err != io.EOF {
			cntr.Unlock()
			return 0, err
		}

		cntr.SetData(path, name, data)
	}
	cntr.Unlock()

	data += "\n"

	return copyResultBuffer(req.Data, []byte(data))
}

func (h *MaxIntBaseHandler) Write(
	n domain.IOnodeIface,
	req *domain.HandlerRequest) (int, error) {

	logrus.Debugf("Executing %v Write() method", h.Name)

	name := n.Name()
	path := n.Path()
	cntr := req.Container

	newMax := strings.TrimSpace(string(req.Data))
	newMaxInt, err := strconv.Atoi(newMax)
	if err != nil {
		logrus.Errorf("Unexpected error: %v", err)
		return 0, err
	}

	// Ensure operation is generated from within a registered sys container.
	if cntr == nil {
		logrus.Errorf("Could not find the container originating this request (pid %v)",
			req.Pid)
		return 0, errors.New("Container not found")
	}

	cntr.Lock()
	defer cntr.Unlock()

	// Check if this resource has been initialized for this container. If not,
	// push it to the host FS and store it within the container struct.
	curMax, ok := cntr.Data(path, name)
	if !ok {
		if err := h.pushFile(n, cntr, newMaxInt); err != nil {
			return 0, err
		}

		cntr.SetData(path, name, newMax)

		return len(req.Data), nil
	}

	curMaxInt, err := strconv.Atoi(curMax)
	if err != nil {
		logrus.Errorf("Unexpected error: %v", err)
		return 0, err
	}

	// If new value is lower/equal than the existing one, then let's update this
	// new value into the container struct but not push it down to the kernel.
	if newMaxInt <= curMaxInt {
		cntr.SetData(path, name, newMax)

		return len(req.Data), nil
	}

	// Push new value to the kernel.
	if err := h.pushFile(n, cntr, newMaxInt); err != nil {
		return 0, io.EOF
	}

	// Writing the new value into container-state struct.
	cntr.SetData(path, name, newMax)

	return len(req.Data), nil
}

func (h *MaxIntBaseHandler) ReadDirAll(
	n domain.IOnodeIface,
	req *domain.HandlerRequest) ([]os.FileInfo, error) {

	return nil, nil
}

func (h *MaxIntBaseHandler) fetchFile(
	n domain.IOnodeIface,
	c domain.ContainerIface) (string, error) {

	// We need the per-resource lock since we are about to access the resource on
	// the host FS. See pushFile() for a full explanation.
	h.Lock.Lock()

	// Read from host FS to extract the existing value.
	curHostMax, err := n.ReadLine()
	if err != nil && err != io.EOF {
		h.Lock.Unlock()
		logrus.Errorf("Could not read from file %v", h.Path)
		return "", err
	}

	h.Lock.Unlock()

	// High-level verification to ensure that format is the expected one.
	_, err = strconv.Atoi(curHostMax)
	if err != nil {
		logrus.Errorf("Unexpected content read from file %v, error %v", h.Path, err)
		return "", err
	}

	return curHostMax, nil
}

func (h *MaxIntBaseHandler) pushFile(
	n domain.IOnodeIface,
	c domain.ContainerIface,
	newMaxInt int) error {

	// We need the per-resource lock since we are about to access the resource on
	// the host FS and multiple sys containers could be accessing that same
	// resource concurrently.
	//
	// But that's not sufficient. Some users may deploy sysbox inside a
	// privileged container, and thus can have multiple sysbox instances running
	// concurrently on the same host. If those sysbox instances write conflicting
	// values to a kernel resource that uses this handler (e.g., a sysctl under
	// /proc/sys), a race condition arises that could cause the value to be
	// written to not be the max across all instances.
	//
	// To reduce the chance of this ocurring, in addition to the per-resource
	// lock, we use a heuristic in which we read-after-write to verify the value
	// of the resource is larger or equal to the one we wrote. If it isn't, it
	// means some other agent on the host wrote a smaller value to the resource
	// after we wrote to it, so we must retry the write.
	//
	// When retrying, we wait a small but random amount of time to reduce the
	// chance of hitting the race condition again. And we retry a limited amount
	// of times.
	//
	// Note that this solution works well for resolving race conditions among
	// sysbox instances, but may not address race conditions with other host
	// agents that write to the same sysctl. That's because there is no guarantee
	// that the other host agent will read-after-write and retry as sysbox does.

	h.Lock.Lock()
	defer h.Lock.Unlock()

	retries := 5
	retryDelay := 100 // microsecs

	for i := 0; i < retries; i++ {

		curHostMax, err := n.ReadLine()
		if err != nil && err != io.EOF {
			return err
		}
		curHostMaxInt, err := strconv.Atoi(curHostMax)
		if err != nil {
			logrus.Errorf("Unexpected error: %v", err)
			return err
		}

		// If the existing host value is larger than the new one to configure,
		// then let's just return here as we want to keep the largest value
		// in the host kernel.
		if newMaxInt <= curHostMaxInt {
			return nil
		}

		// When retrying, wait a random delay to reduce chances of a new collision
		if i > 0 {
			d := rand.Intn(retryDelay)
			time.Sleep(time.Duration(d) * time.Microsecond)
		}

		// Push down to host kernel the new (larger) value.
		msg := []byte(strconv.Itoa(newMaxInt))
		err = n.WriteFile(msg)
		if err != nil && !h.Service.IgnoreErrors() {
			logrus.Errorf("Could not write %d to file: %s", newMaxInt, err)
			return err
		}
	}

	return nil
}

func (h *MaxIntBaseHandler) GetName() string {
	return h.Name
}

func (h *MaxIntBaseHandler) GetPath() string {
	return h.Path
}

func (h *MaxIntBaseHandler) GetEnabled() bool {
	return h.Enabled
}

func (h *MaxIntBaseHandler) GetType() domain.HandlerType {
	return h.Type
}

func (h *MaxIntBaseHandler) GetService() domain.HandlerServiceIface {
	return h.Service
}

func (h *MaxIntBaseHandler) SetEnabled(val bool) {
	h.Enabled = val
}

func (h *MaxIntBaseHandler) SetService(hs domain.HandlerServiceIface) {
	h.Service = hs
}

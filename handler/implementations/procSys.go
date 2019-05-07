package implementations

import (
	"errors"
	"io"
	"log"
	"os"
	"strconv"

	"github.com/nestybox/sysvisor/sysvisor-fs/domain"
)

//
// /proc/sys Handler
//
type ProcSysHandler struct {
	Name      string
	Path      string
	Enabled   bool
	Cacheable bool
	Service   domain.HandlerService
}

//func (h *ProcSysHandler) Lookup(n domain.IOnode, i domain.Inode) (*syscall.Stat_t, error) {
func (h *ProcSysHandler) Lookup(n domain.IOnode, pid uint32) (os.FileInfo, error) {

	log.Printf("Executing Lookup() method on %v handler", h.Name)

	return os.Stat(n.Path())
}

func (h *ProcSysHandler) Open(node domain.IOnode) error {

	log.Printf("Executing %v open() method", h.Name)

	return nil
}

func (h *ProcSysHandler) Close(node domain.IOnode) error {

	log.Printf("Executing Close() method on %v handler", h.Name)

	return nil
}

func (h *ProcSysHandler) Read(n domain.IOnode, pid uint32,
	buf []byte, off int64) (int, error) {

	log.Printf("Executing %v read() method", h.Name)

	if off > 0 {
		return 0, io.EOF
	}

	return 0, nil
}

func (h *ProcSysHandler) Write(n domain.IOnode, pid uint32,
	buf []byte) (int, error) {

	return 0, nil
}

func (h *ProcSysHandler) ReadDirAll(n domain.IOnode, pid uint32) ([]os.FileInfo, error) {

	log.Printf("Executing ReadDirAll() method on %v handler", h.Name)

	// Identify the pidNsInode corresponding to this pid.
	ios := h.Service.IOService()
	tmpNode := ios.NewIOnode("", strconv.Itoa(int(pid)), 0)
	pidInode, err := ios.PidNsInode(tmpNode)
	if err != nil {
		return nil, err
	}

	// Find the container-state corresponding to the container hosting this
	// Pid.
	css := h.Service.StateService()
	cntr := css.ContainerLookupByPid(pidInode)
	if cntr == nil {
		log.Printf("Could not find the container originating this request (pidNsInode %v)\n", pidInode)
		return nil, errors.New("Container not found")
	}

	// Create nsenterEvent to initiate interaction with container namespaces.
	event := &nsenterEvent{
		Resource:  n.Path(),
		Pid:       cntr.InitPid(),
		Namespace: []nsType{string(nsTypeNet)},
		ReqMsg: &nsenterMessage{
			Type:    readDirRequest,
			Payload: n.Path(),
		},
	}

	// Launch nsenter-event.
	err = event.launch()
	if err != nil {
		return nil, err
	}

	// Transform event-response payload into a FileInfo slice. Notice that to
	// convert []T1 struct to a []T2 one we must iterate through each element
	// and do the conversion one element at a time.
	dirEntries := event.ResMsg.Payload.([]domain.FileInfo)
	osFileEntries := make([]os.FileInfo, len(dirEntries))
	for i, v := range dirEntries {
		osFileEntries[i] = v
	}

	return osFileEntries, nil
}

func (h *ProcSysHandler) GetName() string {
	return h.Name
}

func (h *ProcSysHandler) GetPath() string {
	return h.Path
}

func (h *ProcSysHandler) GetEnabled() bool {
	return h.Enabled
}

func (h *ProcSysHandler) SetEnabled(val bool) {
	h.Enabled = val
}

func (h *ProcSysHandler) SetService(hs domain.HandlerService) {
	h.Service = hs
}

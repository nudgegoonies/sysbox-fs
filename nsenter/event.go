package nsenter

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/sirupsen/logrus"
	_ "github.com/nestybox/sysbox-runc/libcontainer/nsenter"
	"github.com/nestybox/sysbox-runc/libcontainer/utils"
	"github.com/vishvananda/netlink/nl"
	"golang.org/x/sys/unix"

	"github.com/nestybox/sysbox-fs/domain"
	"github.com/nestybox/sysbox-fs/fuse"
	"github.com/nestybox/sysbox-runc/libcontainer"
)

func init() {
	if len(os.Args) > 1 && os.Args[1] == "nsenter" {
		runtime.GOMAXPROCS(1)
		runtime.LockOSThread()
	}
}

// Pid struct. Utilized by sysbox-runc's nsexec code.
type pid struct {
	Pid           int `json:"pid"`
	PidFirstChild int `json:"pid_first"`
}

//
// NSenterEvent struct serves as a transport abstraction to represent all the
// potential messages that can be exchanged between sysbox-fs master instance
// and secondary (forked) ones. These sysbox-fs' auxiliar instances are
// utilized to carry out actions over namespaced resources, and as such, cannot
// be performed by sysbox-fs' main instance.
//
// Every bidirectional transaction is represented by an event structure
// (nsenterEvent), which holds both 'request' and 'response' messages, as well
// as the context necessary to complete any action demanding inter-namespace
// message exchanges.
//
type NSenterEvent struct {

	// File/Dir being accessed within a container context.
	Resource string `json:"resource"`

	// initPid associated to the targeted container.
	Pid uint32 `json:"pid"`

	// namespace-types to attach to.
	Namespace []domain.NStype `json:"namespace"`

	// Request message to be sent.
	ReqMsg *domain.NSenterMessage `json:"request"`

	// Request message to be received.
	ResMsg *domain.NSenterMessage `json:"response"`
}

type nsenterService struct {
}

func NewNSenterService() domain.NSenterService {
	return &nsenterService{}
}

func (s *nsenterService) NewEvent(
	path string,
	pid uint32,
	ns []domain.NStype,
	req *domain.NSenterMessage,
	res *domain.NSenterMessage) domain.NSenterEventIface {

	return &NSenterEvent{
		Resource:  path,
		Pid:       pid,
		Namespace: ns,
		ReqMsg:    req,
		ResMsg:    res,
	}
}

func (s *nsenterService) LaunchEvent(e domain.NSenterEventIface) error {
	return e.Launch()
}

func (s *nsenterService) ResponseEvent(e domain.NSenterEventIface) *domain.NSenterMessage {
	return e.Response()
}


///////////////////////////////////////////////////////////////////////////////
//
// nsenterEvent methods below execute within the context of sysbox-fs' main
// instance, upon invocation of sysbox-fs' handler logic.
//
///////////////////////////////////////////////////////////////////////////////

//
// Called by sysbox-fs handler routines to parse the response generated
// by sysbox-fs' grand-child processes.
//
func (e *NSenterEvent) processResponse(pipe io.Reader) error {

	var payload json.RawMessage
	nsenterMsg := domain.NSenterMessage{
		Payload: &payload,
	}

	// Read all state received from the incoming pipe.
	data, err := ioutil.ReadAll(pipe)
	if err != nil || data == nil {
		return err
	}

	// Received message will be decoded in two phases. The first unmarshal call
	// takes care of decoding the message-type being received. Based on the
	// obtained type, we are able to decode the payload generated by the
	// remote-end. This second step is executed as part of a subsequent
	// unmarshal instruction (see further below).
	if err = json.Unmarshal(data, &nsenterMsg); err != nil {
		logrus.Errorf("Error decoding received nsenterMsg response: %v", err)
		return errors.New("Error decoding received event response")
	}

	switch nsenterMsg.Type {

	case domain.LookupResponse:
		logrus.Debug("Received nsenterEvent lookupResponse message")

		var p domain.FileInfo
		if payload != nil {
			err := json.Unmarshal(payload, &p)
			if err != nil {
				logrus.Error(err)
				return err
			}
		}

		e.ResMsg = &domain.NSenterMessage{
			Type:    nsenterMsg.Type,
			Payload: p,
		}
		break

	case domain.OpenFileResponse:
		logrus.Debug("Received nsenterEvent OpenResponse message")

		var p int
		if payload != nil {
			err := json.Unmarshal(payload, &p)
			if err != nil {
				logrus.Error(err)
				return err
			}
		}

		e.ResMsg = &domain.NSenterMessage{
			Type:    nsenterMsg.Type,
			Payload: p,
		}
		break

	case domain.ReadFileResponse:
		logrus.Debug("Received nsenterEvent readResponse message")

		var p string
		if payload != nil {
			err := json.Unmarshal(payload, &p)
			if err != nil {
				logrus.Error(err)
				return err
			}
		}

		e.ResMsg = &domain.NSenterMessage{
			Type:    nsenterMsg.Type,
			Payload: p,
		}
		break

	case domain.WriteFileResponse:
		logrus.Debug("Received nsenterEvent writeResponse message")

		e.ResMsg = &domain.NSenterMessage{
			Type:    nsenterMsg.Type,
			Payload: "",
		}
		break

	case domain.ReadDirResponse:
		logrus.Debug("Received nsenterEvent readDirAllResponse message")

		var p []domain.FileInfo
		if payload != nil {
			err := json.Unmarshal(payload, &p)
			if err != nil {
				logrus.Error(err)
				return err
			}
		}

		e.ResMsg = &domain.NSenterMessage{
			Type:    nsenterMsg.Type,
			Payload: p,
		}
		break

	case domain.ErrorResponse:
		logrus.Debug("Received nsenterEvent errorResponse message")

		var p fuse.IOerror

		if payload != nil {
			err := json.Unmarshal(payload, &p)
			if err != nil {
				logrus.Error(err)
				return err
			}
		}

		e.ResMsg = &domain.NSenterMessage{
			Type:    nsenterMsg.Type,
			Payload: p,
		}
		break

	default:
		return errors.New("Received unsupported nsenterEvent message")
	}

	return nil
}

//
// Auxiliar function to obtain the FS path associated to any given namespace.
// Theese FS paths are utilized by sysbox-runc's nsexec logic to enter the
// desired namespaces.
//
// Expected format example: "mnt:/proc/<pid>/ns/mnt"
//
func (e *NSenterEvent) namespacePaths() []string {

	var paths []string

	for _, nstype := range e.Namespace {
		path := filepath.Join(
			nstype,
			":/proc/",
			strconv.Itoa(int(e.Pid)), "/ns/",
			nstype)
		paths = append(paths, path)
	}

	return paths
}

//
// Sysbox-fs' requests are generated through this method. Handlers seeking to
// access namespaced resources will call this method to invoke sysbox-runc's
// nsexec logic, which will serve to enter the container namespaces that host
// these resources.
//
func (e *NSenterEvent) Launch() error {

	logrus.Debug("Executing nsenterEvent's launch() method")

	// Create a socket pair.
	parentPipe, childPipe, err := utils.NewSockPair("nsenterPipe")
	if err != nil {
		return errors.New("Error creating sysbox-fs nsenter pipe")
	}
	defer parentPipe.Close()

	// Obtain the FS path for all the namespaces to be nsenter'ed into, and
	// define the associated netlink-payload to transfer to child process.
	namespaces := e.namespacePaths()

	r := nl.NewNetlinkRequest(int(libcontainer.InitMsg), 0)
	r.AddData(&libcontainer.Bytemsg{
		Type:  libcontainer.NsPathsAttr,
		Value: []byte(strings.Join(namespaces, ",")),
	})

	// Prepare exec.cmd in charged of running: "sysbox-fs nsenter".
	cmd := &exec.Cmd{
		Path:       "/proc/self/exe",
		Args:       []string{os.Args[0], "nsenter"},
		ExtraFiles: []*os.File{childPipe},
		Env:        []string{"_LIBCONTAINER_INITPIPE=3", fmt.Sprintf("GOMAXPROCS=%s", os.Getenv("GOMAXPROCS"))},
		Stdin:      nil,
		Stdout:     nil,
		Stderr:     nil,
	}

	// Launch sysbox-fs' first child process.
	err = cmd.Start()
	childPipe.Close()
	if err != nil {
		return errors.New("Error launching sysbox-fs first child process")
	}

	// Send the config to child process.
	if _, err := io.Copy(parentPipe, bytes.NewReader(r.Serialize())); err != nil {
		return errors.New("Error copying payload to pipe")
	}

	// Wait for sysbox-fs' first child process to finish.
	status, err := cmd.Process.Wait()
	if err != nil {
		cmd.Wait()
		return err
	}
	if !status.Success() {
		cmd.Wait()
		return errors.New("Error waiting for sysbox-fs first child process")
	}

	// Receive sysbox-fs' first-child pid.
	var pid pid
	decoder := json.NewDecoder(parentPipe)
	if err := decoder.Decode(&pid); err != nil {
		cmd.Wait()
		return errors.New("Error receiving first-child pid")
	}

	firstChildProcess, err := os.FindProcess(pid.PidFirstChild)
	if err != nil {
		return err
	}

	// Wait for sysbox-fs' second child process to finish. Ignore the error in
	// case the child has already been reaped for any reason.
	_, _ = firstChildProcess.Wait()

	// Sysbox-fs' third child (grand-child) process remains and will enter the
	// go runtime.
	process, err := os.FindProcess(pid.Pid)
	if err != nil {
		return err
	}
	cmd.Process = process

	// Transfer the nsenterEvent details to grand-child for processing.
	if err := utils.WriteJSON(parentPipe, e); err != nil {
		return errors.New("Error writing nsenterEvent through pipe")
	}

	// Wait for sysbox-fs' grand-child response and process it accordingly.
	ierr := e.processResponse(parentPipe)

	// Destroy the socket pair.
	if err := unix.Shutdown(int(parentPipe.Fd()), unix.SHUT_WR); err != nil {
		return errors.New("Shutting down sysbox-fs nsenter pipe")
	}

	if ierr != nil {
		cmd.Wait()
		return ierr
	}

	// Wait for grand-child exit()
	cmd.Wait()

	return nil
}

func (e *NSenterEvent) Response() *domain.NSenterMessage {

	return e.ResMsg
}

///////////////////////////////////////////////////////////////////////////////
//
// nsenterEvent methods below execute within the context of container
// namespaces. In other words, they are invoked as part of "sysbox-fs nsenter"
// execution.
//
///////////////////////////////////////////////////////////////////////////////

func (e *NSenterEvent) processLookupRequest() error {

	path := e.Resource

	// Verify if the resource being looked up is reachable and obtain FileInfo
	// details.
	info, err := os.Stat(path)
	if err != nil {
		// Send an error-message response.
		e.ResMsg = &domain.NSenterMessage{
			Type:    domain.ErrorResponse,
			Payload: &fuse.IOerror{RcvError: err},
		}

		return nil
	}

	// Allocate new FileInfo struct to return to sysbpx-fs' main instance.
	fileInfo := domain.FileInfo{
		Fname:    info.Name(),
		Fsize:    info.Size(),
		Fmode:    info.Mode(),
		FmodTime: info.ModTime(),
		FisDir:   info.IsDir(),
		Fsys:     info.Sys().(*syscall.Stat_t),
	}

	// Create a response message.
	e.ResMsg = &domain.NSenterMessage{
		Type:    domain.LookupResponse,
		Payload: fileInfo,
	}

	return nil
}

func (e *NSenterEvent) processOpenFileRequest() error {

	// Extract openflags from the incoming payload.
	openFlags, err := strconv.Atoi(e.ReqMsg.Payload.(string))
	if err != nil {
		return nil
	}

	// Open the file in question. Notice that we are hardcoding the 'mode'
	// argument (third one) as this one is not relevant in a procfs; that
	// is, user cannot create files -- openflags 'O_CREAT' and 'O_TMPFILE'
	// are not expected (refer to "man open(2)" for details).
	_, err = os.OpenFile(e.Resource, openFlags, 0)
	if err != nil {
		// Send an error-message response.
		e.ResMsg = &domain.NSenterMessage{
			Type:    domain.ErrorResponse,
			Payload: &fuse.IOerror{RcvError: err},
		}

		return nil
	}

	// Create a response message.
	e.ResMsg = &domain.NSenterMessage{
		Type:    domain.OpenFileResponse,
		Payload: nil,
	}

	return nil
}
func (e *NSenterEvent) processFileReadRequest() error {

	// Perform read operation and return error msg should this one fail.
	fileContent, err := ioutil.ReadFile(e.Resource)
	if err != nil {
		// Send an error-message response.
		e.ResMsg = &domain.NSenterMessage{
			Type:    domain.ErrorResponse,
			Payload: &fuse.IOerror{RcvError: err},
		}

		return err
	}

	// Create a response message.
	e.ResMsg = &domain.NSenterMessage{
		Type:    domain.ReadFileResponse,
		Payload: strings.TrimSpace(string(fileContent)),
	}

	return nil
}

func (e *NSenterEvent) processFileWriteRequest() error {

	payload := []byte(e.ReqMsg.Payload.(string))

	// Perform write operation and return error msg should this one fail.
	err := ioutil.WriteFile(e.Resource, payload, 0644)
	if err != nil {
		// Send an error-message response.
		e.ResMsg = &domain.NSenterMessage{
			Type:    domain.ErrorResponse,
			Payload: &fuse.IOerror{RcvError: err},
		}

		return nil
	}

	// Create a response message.
	e.ResMsg = &domain.NSenterMessage{
		Type:    domain.WriteFileResponse,
		Payload: nil,
	}

	return nil
}

func (e *NSenterEvent) processDirReadRequest() error {

	// Perform readDir operation and return error msg should this one fail.
	dirContent, err := ioutil.ReadDir(e.Resource)
	if err != nil {
		logrus.Errorf("Error reading from %s resource", e.Resource)
		e.ResMsg = &domain.NSenterMessage{
			Type:    domain.ErrorResponse,
			Payload: err.Error(),
		}

		return err
	}

	// Create a FileInfo slice to return to sysbox-fs' main instance.
	var dirContentList []domain.FileInfo

	for _, entry := range dirContent {
		elem := domain.FileInfo{
			Fname:    entry.Name(),
			Fsize:    entry.Size(),
			Fmode:    entry.Mode(),
			FmodTime: entry.ModTime(),
			FisDir:   entry.IsDir(),
			Fsys:     entry.Sys().(*syscall.Stat_t),
		}
		dirContentList = append(dirContentList, elem)
	}

	// Create a response message.
	e.ResMsg = &domain.NSenterMessage{
		Type:    domain.ReadDirResponse,
		Payload: dirContentList,
	}

	return nil
}

// Method in charge of processing all requests generated by sysbox-fs' master
// instance.
func (e *NSenterEvent) processRequest(pipe io.Reader) error {

	// Decode message into our own nsenterEvent struct.
	if err := json.NewDecoder(pipe).Decode(&e); err != nil {
		return errors.New("Error decoding received nsenterEvent request")
	}

	switch e.ReqMsg.Type {

	case domain.LookupRequest:
		return e.processLookupRequest()

	case domain.OpenFileRequest:
		return e.processOpenFileRequest()

	case domain.ReadFileRequest:
		return e.processFileReadRequest()

	case domain.WriteFileRequest:
		return e.processFileWriteRequest()

	case domain.ReadDirRequest:
		return e.processDirReadRequest()

	default:
		e.ResMsg = &domain.NSenterMessage{
			Type:    domain.ErrorResponse,
			Payload: "Unsupported request",
		}
	}

	return nil
}

//
// Sysbox-fs' post-nsexec initialization function. To be executed within the
// context of one (or more) container namespaces.
//
func Init() (err error) {	

	var (
		pipefd      int
		envInitPipe = os.Getenv("_LIBCONTAINER_INITPIPE")
	)

	// Get the INITPIPE.
	pipefd, err = strconv.Atoi(envInitPipe)
	if err != nil {
		return fmt.Errorf("Unable to convert _LIBCONTAINER_INITPIPE=%s to int: %s",
			envInitPipe, err)
	}

	var pipe = os.NewFile(uintptr(pipefd), "pipe")
	defer pipe.Close()

	// Clear the current process's environment to clean any libcontainer
	// specific env vars.
	os.Clearenv()

	var event NSenterEvent
	err = event.processRequest(pipe)
	if err != nil {
		return err
	}

	// Encode / push response back to sysbox-main.
	data, err := json.Marshal(*(event.ResMsg))
	if err != nil {
		return err
	}
	_, err = pipe.Write(data)

	return nil
}

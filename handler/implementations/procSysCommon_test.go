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

package implementations_test

import (
	"errors"
	"io/ioutil"
	"os"
	"reflect"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/nestybox/sysbox-fs/domain"
	"github.com/nestybox/sysbox-fs/handler/implementations"
	"github.com/nestybox/sysbox-fs/mocks"
	"github.com/nestybox/sysbox-fs/mount"
	"github.com/nestybox/sysbox-fs/nsenter"
	"github.com/nestybox/sysbox-fs/process"
	"github.com/nestybox/sysbox-fs/state"
	"github.com/nestybox/sysbox-fs/sysio"
	"github.com/sirupsen/logrus"
)

// Sysbox-fs global services for all handler's testing consumption.
var css domain.ContainerStateServiceIface
var mts domain.MountServiceIface
var ios domain.IOServiceIface
var prs domain.ProcessServiceIface
var nss *mocks.NSenterServiceIface
var hds *mocks.HandlerServiceIface

func TestMain(m *testing.M) {

	// Disable log generation during UT.
	logrus.SetOutput(ioutil.Discard)

	//
	// Test-cases common settings.
	//
	//
	ios = sysio.NewIOService(domain.IOMemFileService)
	prs = process.NewProcessService()
	nss = &mocks.NSenterServiceIface{}
	hds = &mocks.HandlerServiceIface{}
	css = state.NewContainerStateService()
	mts = mount.NewMountService()

	prs.Setup(ios)
	css.Setup(nil, prs, ios, mts)
	mts.Setup(css, hds, prs, nss)

	// HandlerService's common mocking instructions.
	hds.On("NSenterService").Return(nss)
	hds.On("ProcessService").Return(prs)
	hds.On("DirHandlerEntries", "/proc/sys/net").Return(nil)

	// Run test-suite.
	m.Run()
}

func TestProcSysCommonHandler_Lookup(t *testing.T) {
	type fields struct {
		Name      string
		Path      string
		Type      domain.HandlerType
		Enabled   bool
		Cacheable bool
		Service   domain.HandlerServiceIface
	}

	var f1 = fields{
		Name:      "procSysCommon",
		Path:      "procSysCommonHandler",
		Enabled:   true,
		Cacheable: true,
		Service:   hds,
	}

	type args struct {
		n   domain.IOnodeIface
		req *domain.HandlerRequest
	}

	// Valid method arguments.
	var a1 = args{
		n: ios.NewIOnode("net", "/proc/sys/net", 0),
		req: &domain.HandlerRequest{
			Pid: 1001,
			Container: css.ContainerCreate(
				"c1",
				uint32(1001),
				time.Time{},
				231072,
				65535,
				231072,
				65535,
				nil,
				nil,
				nil),
		},
	}

	// Invalid method arguments -- missing sys-container attribute.
	var a2 = args{
		n: ios.NewIOnode("net", "/proc/sys/net", 0),
		req: &domain.HandlerRequest{
			Pid: 1001,
		},
	}

	tests := []struct {
		name       string
		fields     fields
		args       args
		want       os.FileInfo
		wantErr    bool
		wantErrVal error
		prepare    func()
	}{
		{
			//
			// Test-case 1: Regular Lookup operation. No errors expected.
			//
			name:       "1",
			fields:     f1,
			args:       a1,
			want:       domain.FileInfo{Fname: a1.n.Path()},
			wantErr:    false,
			wantErrVal: nil,
			prepare: func() {

				// Expected nsenter request.
				nsenterEventReq := &nsenter.NSenterEvent{
					Pid:       a1.req.Pid,
					Namespace: &domain.AllNSsButMount,
					ReqMsg: &domain.NSenterMessage{
						Type:    domain.LookupRequest,
						Payload: &domain.LookupPayload{a1.n.Path()},
					},
				}

				// Expected nsenter response.
				nsenterEventResp := &nsenter.NSenterEvent{
					ResMsg: &domain.NSenterMessage{
						Type: domain.LookupResponse,
						Payload: domain.FileInfo{
							Fname: a1.n.Path()},
					},
				}

				nss.On(
					"NewEvent",
					a1.req.Pid,
					&domain.AllNSsButMount,
					nsenterEventReq.ReqMsg,
					(*domain.NSenterMessage)(nil),
					false).Return(nsenterEventReq)

				nss.On("SendRequestEvent", nsenterEventReq).Return(nil)
				nss.On("ReceiveResponseEvent", nsenterEventReq).Return(nsenterEventResp.ResMsg)
			},
		},
		{
			//
			// Test-case 2: Verify proper behavior if an invalid handlerReq is
			// received -- missing sys-container attribute.
			//
			name:       "2",
			fields:     f1,
			args:       a2,
			want:       nil,
			wantErr:    true,
			wantErrVal: errors.New("Container not found"),
			prepare:    func() {},
		},
		{
			//
			// Test-case 3: Verify proper behavior during nsenter error conditions
			// (EACCESS).
			//
			name:       "3",
			fields:     f1,
			args:       a1,
			want:       nil,
			wantErr:    true,
			wantErrVal: syscall.EACCES,
			prepare: func() {

				// Expected nsenter request.
				nsenterEventReq := &nsenter.NSenterEvent{
					Pid:       a1.req.Pid,
					Namespace: &domain.AllNSsButMount,
					ReqMsg: &domain.NSenterMessage{
						Type:    domain.LookupRequest,
						Payload: &domain.LookupPayload{a1.n.Path()},
					},
				}

				// Expected nsenter response.
				nsenterEventResp := &nsenter.NSenterEvent{
					ResMsg: &domain.NSenterMessage{
						Type:    domain.ErrorResponse,
						Payload: syscall.Errno(syscall.EACCES),
					},
				}

				nss.On(
					"NewEvent",
					a1.req.Pid,
					&domain.AllNSsButMount,
					nsenterEventReq.ReqMsg,
					(*domain.NSenterMessage)(nil),
					false).Return(nsenterEventReq)

				nss.On("SendRequestEvent", nsenterEventReq).Return(nil)
				nss.On("ReceiveResponseEvent", nsenterEventReq).Return(nsenterEventResp.ResMsg)
			},
		},
	}

	//
	// Testcase executions.
	//
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &implementations.ProcSysCommonHandler{
				domain.HandlerBase{
					Name:      tt.fields.Name,
					Path:      tt.fields.Path,
					Type:      tt.fields.Type,
					Enabled:   tt.fields.Enabled,
					Cacheable: tt.fields.Cacheable,
					Service:   tt.fields.Service,
				},
			}

			// Prepare the mocks.
			if tt.prepare != nil {
				tt.prepare()
			}

			got, err := h.Lookup(tt.args.n, tt.args.req)
			if (err != nil) != tt.wantErr {
				t.Errorf("ProcSysCommonHandler.Lookup() error = %v, wantErr %v",
					err, tt.wantErr)
				return
			}
			if err != nil && tt.wantErrVal != nil && err.Error() != tt.wantErrVal.Error() {
				t.Errorf("ProcSysCommonHandler.Lookup() error = %v, wantErr %v, wantErrVal %v",
					err, tt.wantErr, tt.wantErrVal)
				return
			}

			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ProcSysCommonHandler.Lookup() = %v, want %v", got, tt.want)
			}

			// Ensure that mocks were properly invoked and reset expectedCalls
			// object.
			nss.AssertExpectations(t)
			nss.ExpectedCalls = nil
		})
	}
}

func TestProcSysCommonHandler_Getattr(t *testing.T) {
	type fields struct {
		Name      string
		Path      string
		Type      domain.HandlerType
		Enabled   bool
		Cacheable bool
		Service   domain.HandlerServiceIface
	}

	var f1 = fields{
		Name:      "procSysCommon",
		Path:      "procSysCommonHandler",
		Enabled:   true,
		Cacheable: true,
		Service:   hds,
	}

	type args struct {
		n   domain.IOnodeIface
		req *domain.HandlerRequest
	}

	// Valid method arguments.
	var a1 = args{
		n: ios.NewIOnode("net", "/proc/sys/net", 0),
		req: &domain.HandlerRequest{
			Pid: 1001,
			Container: css.ContainerCreate(
				"c1",
				uint32(1001),
				time.Time{},
				231072,
				65535,
				231072,
				65535,
				nil,
				nil,
				nil),
		},
	}

	// Invalid method arguments -- missing sys-container attribute.
	var a2 = args{
		n: ios.NewIOnode("net", "/proc/sys/net", 0),
		req: &domain.HandlerRequest{
			Pid: 1001,
		},
	}

	tests := []struct {
		name       string
		fields     fields
		args       args
		want       *syscall.Stat_t
		wantErr    bool
		wantErrVal error
		prepare    func()
	}{
		{
			//
			// Test-case 1: Regular Getattr operation. No errors expected.
			//
			name:       "1",
			fields:     f1,
			args:       a1,
			want:       &syscall.Stat_t{Uid: 231072, Gid: 231072},
			wantErr:    false,
			wantErrVal: nil,
			prepare:    func() {},
		},
		{
			//
			// Test-case 2: Verify proper behavior if an invalid handlerReq is
			// received -- missing sys-container attribute.
			//
			name:       "2",
			fields:     f1,
			args:       a2,
			want:       nil,
			wantErr:    true,
			wantErrVal: errors.New("Container not found"),
			prepare:    func() {},
		},
	}

	//
	// Testcase executions.
	//
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &implementations.ProcSysCommonHandler{
				domain.HandlerBase{
					Name:      tt.fields.Name,
					Path:      tt.fields.Path,
					Type:      tt.fields.Type,
					Enabled:   tt.fields.Enabled,
					Cacheable: tt.fields.Cacheable,
					Service:   tt.fields.Service,
				},
			}

			// Prepare the mocks.
			if tt.prepare != nil {
				tt.prepare()
			}

			got, err := h.Getattr(tt.args.n, tt.args.req)
			if (err != nil) != tt.wantErr {
				t.Errorf("ProcSysCommonHandler.Getattr() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && tt.wantErrVal != nil && err.Error() != tt.wantErrVal.Error() {
				t.Errorf("ProcSysCommonHandler.Lookup() error = %v, wantErr %v, wantErrVal %v",
					err, tt.wantErr, tt.wantErrVal)
			}

			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ProcSysCommonHandler.Getattr() = %v, want %v", got, tt.want)
			}

			// Ensure that mocks were properly invoked and reset expectedCalls
			// object.
			nss.AssertExpectations(t)
			nss.ExpectedCalls = nil
		})
	}
}

func TestProcSysCommonHandler_Open(t *testing.T) {
	type fields struct {
		Name      string
		Path      string
		Type      domain.HandlerType
		Enabled   bool
		Cacheable bool
		Service   domain.HandlerServiceIface
	}

	var f1 = fields{
		Name:      "procSysCommon",
		Path:      "procSysCommonHandler",
		Enabled:   true,
		Cacheable: true,
		Service:   hds,
	}

	type args struct {
		n   domain.IOnodeIface
		req *domain.HandlerRequest
	}

	// Valid method arguments.
	var a1 = args{
		n: ios.NewIOnode("net", "/proc/sys/net", 0),
		req: &domain.HandlerRequest{
			Pid: 1001,
			Container: css.ContainerCreate(
				"c1",
				uint32(1001),
				time.Time{},
				231072,
				65535,
				231072,
				65535,
				nil,
				nil,
				nil),
		},
	}

	// Invalid method arguments -- missing sys-container attribute.
	var a2 = args{
		n: ios.NewIOnode("net", "/proc/sys/net", 0),
		req: &domain.HandlerRequest{
			Pid: 1001,
		},
	}

	tests := []struct {
		name       string
		fields     fields
		args       args
		wantErr    bool
		wantErrVal error
		prepare    func()
	}{
		{
			//
			// Test-case 1: Regular Open operation. No errors expected.
			//
			name:       "1",
			fields:     f1,
			args:       a1,
			wantErr:    false,
			wantErrVal: nil,
			prepare: func() {

				// Expected nsenter request.
				nsenterEventReq := &nsenter.NSenterEvent{
					Pid:       a1.req.Pid,
					Namespace: &domain.AllNSsButMount,
					ReqMsg: &domain.NSenterMessage{
						Type: domain.OpenFileRequest,
						Payload: &domain.OpenFilePayload{
							File:  a1.n.Path(),
							Flags: strconv.Itoa(a1.n.OpenFlags()),
							Mode:  strconv.Itoa(int(a1.n.OpenMode()))},
					},
				}

				// Expected nsenter response.
				nsenterEventResp := &nsenter.NSenterEvent{
					ResMsg: &domain.NSenterMessage{
						Type:    domain.OpenFileResponse,
						Payload: nil,
					},
				}

				nss.On(
					"NewEvent",
					a1.req.Pid,
					&domain.AllNSsButMount,
					nsenterEventReq.ReqMsg,
					(*domain.NSenterMessage)(nil),
					false).Return(nsenterEventReq)

				nss.On("SendRequestEvent", nsenterEventReq).Return(nil)
				nss.On("ReceiveResponseEvent", nsenterEventReq).Return(nsenterEventResp.ResMsg)
			},
		},
		{
			//
			// Test-case 2: Verify proper behavior if an invalid handlerReq is
			// received -- missing sys-container attribute.
			//
			name:       "2",
			fields:     f1,
			args:       a2,
			wantErr:    true,
			wantErrVal: errors.New("Container not found"),
			prepare:    func() {},
		},
		{
			//
			// Test-case 3: Verify proper behavior during nsenter error conditions
			// (EACCESS).
			//
			name:       "3",
			fields:     f1,
			args:       a1,
			wantErr:    true,
			wantErrVal: syscall.EPERM,
			prepare: func() {

				// Expected nsenter request.
				nsenterEventReq := &nsenter.NSenterEvent{
					Pid:       a1.req.Pid,
					Namespace: &domain.AllNSsButMount,
					ReqMsg: &domain.NSenterMessage{
						Type: domain.OpenFileRequest,
						Payload: &domain.OpenFilePayload{
							File:  a1.n.Path(),
							Flags: strconv.Itoa(a1.n.OpenFlags()),
							Mode:  strconv.Itoa(int(a1.n.OpenMode()))},
					},
				}

				// Expected nsenter response.
				nsenterEventResp := &nsenter.NSenterEvent{
					ResMsg: &domain.NSenterMessage{
						Type:    domain.ErrorResponse,
						Payload: syscall.Errno(syscall.EPERM),
					},
				}

				nss.On(
					"NewEvent",
					a1.req.Pid,
					&domain.AllNSsButMount,
					nsenterEventReq.ReqMsg,
					(*domain.NSenterMessage)(nil),
					false).Return(nsenterEventReq)

				nss.On("SendRequestEvent", nsenterEventReq).Return(nil)
				nss.On("ReceiveResponseEvent", nsenterEventReq).Return(nsenterEventResp.ResMsg)
			},
		},
	}

	//
	// Testcase executions.
	//
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &implementations.ProcSysCommonHandler{
				domain.HandlerBase{
					Name:      tt.fields.Name,
					Path:      tt.fields.Path,
					Type:      tt.fields.Type,
					Enabled:   tt.fields.Enabled,
					Cacheable: tt.fields.Cacheable,
					Service:   tt.fields.Service,
				},
			}

			// Prepare the mocks.
			if tt.prepare != nil {
				tt.prepare()
			}

			err := h.Open(tt.args.n, tt.args.req)
			if (err != nil) != tt.wantErr {
				t.Errorf("ProcSysCommonHandler.Open() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && tt.wantErrVal != nil && err.Error() != tt.wantErrVal.Error() {
				t.Errorf("ProcSysCommonHandler.Lookup() error = %v, wantErr %v, wantErrVal %v",
					err, tt.wantErr, tt.wantErrVal)
			}

			// Ensure that mocks were properly invoked and reset expectedCalls
			// object.
			nss.AssertExpectations(t)
			nss.ExpectedCalls = nil
		})
	}
}

func TestProcSysCommonHandler_Read(t *testing.T) {
	type fields struct {
		Name      string
		Path      string
		Type      domain.HandlerType
		Enabled   bool
		Cacheable bool
		Service   domain.HandlerServiceIface
	}

	// Caching enabled.
	var f1 = fields{
		Name:      "procSysCommon",
		Path:      "procSysCommonHandler",
		Enabled:   true,
		Cacheable: true,
		Service:   hds,
	}

	// Caching disabled. Utilized in Testcase-3 to force nsenter error condition.
	var f2 = fields{
		Name:      "procSysCommon",
		Path:      "procSysCommonHandler",
		Enabled:   true,
		Cacheable: false,
		Service:   hds,
	}

	type args struct {
		n   domain.IOnodeIface
		req *domain.HandlerRequest
	}

	// Valid method arguments.
	var a1 = args{
		n: ios.NewIOnode("node_1", "/proc/sys/net/node_1", 0),
		req: &domain.HandlerRequest{
			Pid:  1001,
			Data: make([]byte, len(string("file content 0123456789"))),
			Container: css.ContainerCreate(
				"c1",
				uint32(1001),
				time.Time{},
				231072,
				65535,
				231072,
				65535,
				nil,
				nil,
				css),
		},
	}

	// Invalid method arguments -- missing sys-container attribute.
	var a2 = args{
		n: ios.NewIOnode("node_1", "/proc/sys/net/node_1", 0),
		req: &domain.HandlerRequest{
			Pid: 1001,
		},
	}

	tests := []struct {
		name       string
		fields     fields
		args       args
		want       int
		wantErr    bool
		wantErrVal error
		prepare    func()
	}{
		{
			//
			// Test-case 1: Regular Read operation. No errors expected.
			//
			name:       "1",
			fields:     f1,
			args:       a1,
			want:       len(string("file content 0123456789")),
			wantErr:    false,
			wantErrVal: nil,
			prepare: func() {

				// Setup dynamic state associated to tested container.
				c1 := a1.req.Container
				_ = c1.SetInitProc(c1.InitPid(), c1.UID(), c1.GID())
				c1.InitProc().CreateNsInodes(123456)

				// Expected nsenter request.
				nsenterEventReq := &nsenter.NSenterEvent{
					Pid:       a1.req.Pid,
					Namespace: &domain.AllNSsButMount,
					ReqMsg: &domain.NSenterMessage{
						Type: domain.ReadFileRequest,
						Payload: &domain.ReadFilePayload{
							File: a1.n.Path(),
						},
					},
				}

				// Expected nsenter response.
				nsenterEventResp := &nsenter.NSenterEvent{
					ResMsg: &domain.NSenterMessage{
						Type:    domain.ReadFileResponse,
						Payload: string("file content 0123456789"),
					},
				}

				nss.On(
					"NewEvent",
					a1.req.Pid,
					&domain.AllNSsButMount,
					nsenterEventReq.ReqMsg,
					(*domain.NSenterMessage)(nil),
					false).Return(nsenterEventReq)

				nss.On("SendRequestEvent", nsenterEventReq).Return(nil)
				nss.On("ReceiveResponseEvent", nsenterEventReq).Return(nsenterEventResp.ResMsg)
			},
		},
		{
			//
			// Test-case 2: Verify proper behavior if an invalid handlerReq is
			// received -- missing sys-container attribute.
			//
			name:       "2",
			fields:     f1,
			args:       a2,
			want:       0,
			wantErr:    true,
			wantErrVal: errors.New("Container not found"),
			prepare:    func() {},
		},
		{
			//
			// Test-case 3: Verify proper behavior during nsenter error conditions
			// (EACCESS).
			//
			name:       "3",
			fields:     f2,
			args:       a1,
			want:       0,
			wantErr:    true,
			wantErrVal: syscall.EACCES,
			prepare: func() {

				// Setup dynamic state associated to tested container.
				c1 := a1.req.Container
				_ = c1.SetInitProc(c1.InitPid(), c1.UID(), c1.GID())
				c1.InitProc().CreateNsInodes(123456)

				// Expected nsenter request.
				nsenterEventReq := &nsenter.NSenterEvent{
					Pid:       a1.req.Pid,
					Namespace: &domain.AllNSsButMount,
					ReqMsg: &domain.NSenterMessage{
						Type: domain.ReadFileRequest,
						Payload: &domain.ReadFilePayload{
							File: a1.n.Path(),
						},
					},
				}

				// Expected nsenter response.
				nsenterEventResp := &nsenter.NSenterEvent{
					ResMsg: &domain.NSenterMessage{
						Type:    domain.ErrorResponse,
						Payload: syscall.Errno(syscall.EACCES),
					},
				}

				nss.On(
					"NewEvent",
					a1.req.Pid,
					&domain.AllNSsButMount,
					nsenterEventReq.ReqMsg,
					(*domain.NSenterMessage)(nil),
					false).Return(nsenterEventReq)

				nss.On("SendRequestEvent", nsenterEventReq).Return(nil)
				nss.On("ReceiveResponseEvent", nsenterEventReq).Return(nsenterEventResp.ResMsg)
			},
		},
	}

	//
	// Testcase executions.
	//
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &implementations.ProcSysCommonHandler{
				domain.HandlerBase{
					Name:      tt.fields.Name,
					Path:      tt.fields.Path,
					Type:      tt.fields.Type,
					Enabled:   tt.fields.Enabled,
					Cacheable: tt.fields.Cacheable,
					Service:   tt.fields.Service,
				},
			}

			// Prepare the mocks.
			if tt.prepare != nil {
				tt.prepare()
			}

			got, err := h.Read(tt.args.n, tt.args.req)
			if (err != nil) != tt.wantErr {
				t.Errorf("ProcSysCommonHandler.Read() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("ProcSysCommonHandler.Read() = %v, want %v", got, tt.want)
			}

			// Ensure that mocks were properly invoked and reset expectedCalls
			// object.
			nss.AssertExpectations(t)
			nss.ExpectedCalls = nil
		})
	}
}

func TestProcSysCommonHandler_Write(t *testing.T) {
	type fields struct {
		Name      string
		Path      string
		Type      domain.HandlerType
		Enabled   bool
		Cacheable bool
		Service   domain.HandlerServiceIface
	}

	var f1 = fields{
		Name:      "procSysCommon",
		Path:      "procSysCommonHandler",
		Enabled:   true,
		Cacheable: true,
		Service:   hds,
	}

	type args struct {
		n   domain.IOnodeIface
		req *domain.HandlerRequest
	}

	// Valid method arguments.
	var a1 = args{
		n: ios.NewIOnode("node_1", "/proc/sys/net/node_1", 0),
		req: &domain.HandlerRequest{
			Pid:  1001,
			Data: []byte(string("file content 0123456789")),
			Container: css.ContainerCreate(
				"c1",
				uint32(1001),
				time.Time{},
				231072,
				65535,
				231072,
				65535,
				nil,
				nil,
				css),
		},
	}

	// Invalid method arguments -- missing sys-container attribute.
	var a2 = args{
		n: ios.NewIOnode("node_1", "/proc/sys/net/node_1", 0),
		req: &domain.HandlerRequest{
			Pid: 1001,
		},
	}

	tests := []struct {
		name       string
		fields     fields
		args       args
		want       int
		wantErr    bool
		wantErrVal error
		prepare    func()
	}{
		{
			//
			// Test-case 1: Regular Write operation. No errors expected.
			//
			name:       "1",
			fields:     f1,
			args:       a1,
			want:       len(string("file content 0123456789")),
			wantErr:    false,
			wantErrVal: nil,
			prepare: func() {

				// Setup dynamic state associated to tested container.
				c1 := a1.req.Container
				_ = c1.SetInitProc(c1.InitPid(), c1.UID(), c1.GID())
				c1.InitProc().CreateNsInodes(123456)

				// Expected nsenter request.
				nsenterEventReq := &nsenter.NSenterEvent{
					Pid:       a1.req.Pid,
					Namespace: &domain.AllNSsButMount,
					ReqMsg: &domain.NSenterMessage{
						Type: domain.WriteFileRequest,
						Payload: &domain.WriteFilePayload{
							File:    a1.n.Path(),
							Content: "file content 0123456789",
						},
					},
				}

				// Expected nsenter response.
				nsenterEventResp := &nsenter.NSenterEvent{
					ResMsg: &domain.NSenterMessage{
						Type:    domain.WriteFileResponse,
						Payload: "file content 0123456789",
					},
				}

				nss.On(
					"NewEvent",
					a1.req.Pid,
					&domain.AllNSsButMount,
					nsenterEventReq.ReqMsg,
					(*domain.NSenterMessage)(nil),
					false).Return(nsenterEventReq)

				nss.On("SendRequestEvent", nsenterEventReq).Return(nil)
				nss.On("ReceiveResponseEvent", nsenterEventReq).Return(nsenterEventResp.ResMsg)
			},
		},
		{
			//
			// Test-case 2: Verify proper behavior if an invalid handlerReq is
			// received -- missing sys-container attribute.
			//
			name:       "2",
			fields:     f1,
			args:       a2,
			want:       0,
			wantErr:    true,
			wantErrVal: errors.New("Container not found"),
			prepare:    func() {},
		},
		{
			//
			// Test-case 3: Verify proper behavior during nsenter error conditions
			// (EACCESS).
			//
			name:       "3",
			fields:     f1,
			args:       a1,
			want:       0,
			wantErr:    true,
			wantErrVal: syscall.EACCES,
			prepare: func() {

				// Setup dynamic state associated to tested container.
				c1 := a1.req.Container
				_ = c1.SetInitProc(c1.InitPid(), c1.UID(), c1.GID())
				c1.InitProc().CreateNsInodes(123456)

				// Expected nsenter request.
				nsenterEventReq := &nsenter.NSenterEvent{
					Pid:       a1.req.Pid,
					Namespace: &domain.AllNSsButMount,
					ReqMsg: &domain.NSenterMessage{
						Type: domain.WriteFileRequest,
						Payload: &domain.WriteFilePayload{
							File:    a1.n.Path(),
							Content: "file content 0123456789",
						},
					},
				}

				// Expected nsenter response.
				nsenterEventResp := &nsenter.NSenterEvent{
					ResMsg: &domain.NSenterMessage{
						Type:    domain.ErrorResponse,
						Payload: syscall.Errno(syscall.EACCES),
					},
				}

				nss.On(
					"NewEvent",
					a1.req.Pid,
					&domain.AllNSsButMount,
					nsenterEventReq.ReqMsg,
					(*domain.NSenterMessage)(nil),
					false).Return(nsenterEventReq)

				nss.On("SendRequestEvent", nsenterEventReq).Return(nil)
				nss.On("ReceiveResponseEvent", nsenterEventReq).Return(nsenterEventResp.ResMsg)
			},
		},
	}

	//
	// Testcase executions.
	//
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &implementations.ProcSysCommonHandler{
				domain.HandlerBase{
					Name:      tt.fields.Name,
					Path:      tt.fields.Path,
					Type:      tt.fields.Type,
					Enabled:   tt.fields.Enabled,
					Cacheable: tt.fields.Cacheable,
					Service:   tt.fields.Service,
				},
			}

			// Prepare the mocks.
			if tt.prepare != nil {
				tt.prepare()
			}

			got, err := h.Write(tt.args.n, tt.args.req)
			if (err != nil) != tt.wantErr {
				t.Errorf("ProcSysCommonHandler.Write() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("ProcSysCommonHandler.Write() = %v, want %v", got, tt.want)
			}

			// Ensure that mocks were properly invoked and reset expectedCalls
			// object.
			nss.AssertExpectations(t)
			nss.ExpectedCalls = nil
		})
	}
}

func TestProcSysCommonHandler_ReadDirAll(t *testing.T) {
	type fields struct {
		Name      string
		Path      string
		Type      domain.HandlerType
		Enabled   bool
		Cacheable bool
		Service   domain.HandlerServiceIface
	}

	var f1 = fields{
		Name:      "procSysCommon",
		Path:      "procSysCommonHandler",
		Enabled:   true,
		Cacheable: true,
		Service:   hds,
	}

	type args struct {
		n   domain.IOnodeIface
		req *domain.HandlerRequest
	}

	// Valid method arguments.
	var a1 = args{
		n: ios.NewIOnode("net", "/proc/sys/net", 0),
		req: &domain.HandlerRequest{
			Pid: 1001,
			Container: css.ContainerCreate(
				"c1",
				uint32(1001),
				time.Time{},
				231072,
				65535,
				231072,
				65535,
				nil,
				nil,
				css),
		},
	}

	// Invalid method arguments -- missing sys-container attribute.
	var a2 = args{
		n: ios.NewIOnode("net", "/proc/sys/net", 0),
		req: &domain.HandlerRequest{
			Pid: 1001,
		},
	}

	// Expected responses.
	var t1_result = []os.FileInfo{
		domain.FileInfo{
			Fname: "/proc/sys/net/ipv4",
		},
		domain.FileInfo{
			Fname: "/proc/sys/net/ipv6",
		},
	}

	tests := []struct {
		name       string
		fields     fields
		args       args
		want       []os.FileInfo
		wantErr    bool
		wantErrVal error
		prepare    func()
	}{
		{
			//
			// Test-case 1: Regular ReadDirAll operation. No errors expected.
			//
			name:       "1",
			fields:     f1,
			args:       a1,
			want:       t1_result,
			wantErr:    false,
			wantErrVal: nil,
			prepare: func() {

				// Setup dynamic state associated to tested container.
				c1 := a1.req.Container
				_ = c1.SetInitProc(c1.InitPid(), c1.UID(), c1.GID())
				c1.InitProc().CreateNsInodes(123456)

				// Expected nsenter request.
				nsenterEventReq := &nsenter.NSenterEvent{
					Pid:       a1.req.Pid,
					Namespace: &domain.AllNSsButMount,
					ReqMsg: &domain.NSenterMessage{
						Type: domain.ReadDirRequest,
						Payload: &domain.ReadDirPayload{
							Dir: a1.n.Path(),
						},
					},
				}

				// Expected nsenter response.
				nsenterEventResp := &nsenter.NSenterEvent{
					ResMsg: &domain.NSenterMessage{
						Type: domain.ReadDirResponse,
						Payload: []domain.FileInfo{
							domain.FileInfo{
								Fname: "/proc/sys/net/ipv4",
							},
							domain.FileInfo{
								Fname: "/proc/sys/net/ipv6",
							},
						},
					},
				}

				nss.On(
					"NewEvent",
					a1.req.Pid,
					&domain.AllNSsButMount,
					nsenterEventReq.ReqMsg,
					(*domain.NSenterMessage)(nil),
					false).Return(nsenterEventReq)

				nss.On("SendRequestEvent", nsenterEventReq).Return(nil)
				nss.On("ReceiveResponseEvent", nsenterEventReq).Return(nsenterEventResp.ResMsg)
			},
		},
		{
			//
			// Test-case 2: Verify proper behavior if an invalid handlerReq is
			// received -- missing sys-container attribute.
			//
			name:       "2",
			fields:     f1,
			args:       a2,
			want:       nil,
			wantErr:    true,
			wantErrVal: errors.New("Container not found"),
			prepare:    func() {},
		},
		{
			//
			// Test-case 3: Verify proper behavior during nsenter error conditions
			// (EACCESS).
			//
			name:       "3",
			fields:     f1,
			args:       a1,
			want:       nil,
			wantErr:    true,
			wantErrVal: syscall.EACCES,
			prepare: func() {

				// Setup dynamic state associated to tested container.
				c1 := a1.req.Container
				_ = c1.SetInitProc(c1.InitPid(), c1.UID(), c1.GID())
				c1.InitProc().CreateNsInodes(123456)

				// Expected nsenter request.
				nsenterEventReq := &nsenter.NSenterEvent{
					Pid:       a1.req.Pid,
					Namespace: &domain.AllNSsButMount,
					ReqMsg: &domain.NSenterMessage{
						Type: domain.ReadDirRequest,
						Payload: &domain.ReadDirPayload{
							Dir: a1.n.Path(),
						},
					},
				}

				// Expected nsenter response.
				nsenterEventResp := &nsenter.NSenterEvent{
					ResMsg: &domain.NSenterMessage{
						Type:    domain.ErrorResponse,
						Payload: syscall.Errno(syscall.EACCES),
					},
				}

				nss.On(
					"NewEvent",
					a1.req.Pid,
					&domain.AllNSsButMount,
					nsenterEventReq.ReqMsg,
					(*domain.NSenterMessage)(nil),
					false).Return(nsenterEventReq)

				nss.On("SendRequestEvent", nsenterEventReq).Return(nil)
				nss.On("ReceiveResponseEvent", nsenterEventReq).Return(nsenterEventResp.ResMsg)
			},
		},
	}

	//
	// Testcase executions.
	//
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &implementations.ProcSysCommonHandler{
				domain.HandlerBase{
					Name:      tt.fields.Name,
					Path:      tt.fields.Path,
					Type:      tt.fields.Type,
					Enabled:   tt.fields.Enabled,
					Cacheable: tt.fields.Cacheable,
					Service:   tt.fields.Service,
				},
			}

			// Prepare the mocks.
			if tt.prepare != nil {
				tt.prepare()
			}

			got, err := h.ReadDirAll(tt.args.n, tt.args.req)
			if (err != nil) != tt.wantErr {
				t.Errorf("ProcSysCommonHandler.ReadDirAll() error = %v, wantErr %v",
					err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ProcSysCommonHandler.ReadDirAll() = %v, want %v",
					got, tt.want)
			}

			// Ensure that mocks were properly invoked and reset expectedCalls
			// object.
			nss.AssertExpectations(t)
			nss.ExpectedCalls = nil

		})
	}
}

func TestProcSysCommonHandler_Setattr(t *testing.T) {
	type fields struct {
		Name      string
		Path      string
		Type      domain.HandlerType
		Enabled   bool
		Cacheable bool
		Service   domain.HandlerServiceIface
	}
	type args struct {
		n   domain.IOnodeIface
		req *domain.HandlerRequest
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		wantErr bool
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &implementations.ProcSysCommonHandler{
				domain.HandlerBase{
					Name:      tt.fields.Name,
					Path:      tt.fields.Path,
					Type:      tt.fields.Type,
					Enabled:   tt.fields.Enabled,
					Cacheable: tt.fields.Cacheable,
					Service:   tt.fields.Service,
				},
			}
			if err := h.Setattr(tt.args.n, tt.args.req); (err != nil) != tt.wantErr {
				t.Errorf("ProcSysCommonHandler.Setattr() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestProcSysCommonHandler_GetName(t *testing.T) {
	type fields struct {
		Name      string
		Path      string
		Type      domain.HandlerType
		Enabled   bool
		Cacheable bool
		Service   domain.HandlerServiceIface
	}
	tests := []struct {
		name   string
		fields fields
		want   string
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &implementations.ProcSysCommonHandler{
				domain.HandlerBase{
					Name:      tt.fields.Name,
					Path:      tt.fields.Path,
					Type:      tt.fields.Type,
					Enabled:   tt.fields.Enabled,
					Cacheable: tt.fields.Cacheable,
					Service:   tt.fields.Service,
				},
			}
			if got := h.GetName(); got != tt.want {
				t.Errorf("ProcSysCommonHandler.GetName() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestProcSysCommonHandler_GetPath(t *testing.T) {
	type fields struct {
		Name      string
		Path      string
		Type      domain.HandlerType
		Enabled   bool
		Cacheable bool
		Service   domain.HandlerServiceIface
	}
	tests := []struct {
		name   string
		fields fields
		want   string
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &implementations.ProcSysCommonHandler{
				domain.HandlerBase{
					Name:      tt.fields.Name,
					Path:      tt.fields.Path,
					Type:      tt.fields.Type,
					Enabled:   tt.fields.Enabled,
					Cacheable: tt.fields.Cacheable,
					Service:   tt.fields.Service,
				},
			}
			if got := h.GetPath(); got != tt.want {
				t.Errorf("ProcSysCommonHandler.GetPath() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestProcSysCommonHandler_GetEnabled(t *testing.T) {
	type fields struct {
		Name      string
		Path      string
		Type      domain.HandlerType
		Enabled   bool
		Cacheable bool
		Service   domain.HandlerServiceIface
	}
	tests := []struct {
		name   string
		fields fields
		want   bool
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &implementations.ProcSysCommonHandler{
				domain.HandlerBase{
					Name:      tt.fields.Name,
					Path:      tt.fields.Path,
					Type:      tt.fields.Type,
					Enabled:   tt.fields.Enabled,
					Cacheable: tt.fields.Cacheable,
					Service:   tt.fields.Service,
				},
			}
			if got := h.GetEnabled(); got != tt.want {
				t.Errorf("ProcSysCommonHandler.GetEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestProcSysCommonHandler_GetType(t *testing.T) {
	type fields struct {
		Name      string
		Path      string
		Type      domain.HandlerType
		Enabled   bool
		Cacheable bool
		Service   domain.HandlerServiceIface
	}
	tests := []struct {
		name   string
		fields fields
		want   domain.HandlerType
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &implementations.ProcSysCommonHandler{
				domain.HandlerBase{
					Name:      tt.fields.Name,
					Path:      tt.fields.Path,
					Type:      tt.fields.Type,
					Enabled:   tt.fields.Enabled,
					Cacheable: tt.fields.Cacheable,
					Service:   tt.fields.Service,
				},
			}
			if got := h.GetType(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ProcSysCommonHandler.GetType() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestProcSysCommonHandler_GetService(t *testing.T) {
	type fields struct {
		Name      string
		Path      string
		Type      domain.HandlerType
		Enabled   bool
		Cacheable bool
		Service   domain.HandlerServiceIface
	}
	tests := []struct {
		name   string
		fields fields
		want   domain.HandlerServiceIface
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &implementations.ProcSysCommonHandler{
				domain.HandlerBase{
					Name:      tt.fields.Name,
					Path:      tt.fields.Path,
					Type:      tt.fields.Type,
					Enabled:   tt.fields.Enabled,
					Cacheable: tt.fields.Cacheable,
					Service:   tt.fields.Service,
				},
			}
			if got := h.GetService(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ProcSysCommonHandler.GetService() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestProcSysCommonHandler_SetEnabled(t *testing.T) {
	type fields struct {
		Name      string
		Path      string
		Type      domain.HandlerType
		Enabled   bool
		Cacheable bool
		Service   domain.HandlerServiceIface
	}
	type args struct {
		val bool
	}
	tests := []struct {
		name   string
		fields fields
		args   args
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &implementations.ProcSysCommonHandler{
				domain.HandlerBase{
					Name:      tt.fields.Name,
					Path:      tt.fields.Path,
					Type:      tt.fields.Type,
					Enabled:   tt.fields.Enabled,
					Cacheable: tt.fields.Cacheable,
					Service:   tt.fields.Service,
				},
			}
			h.SetEnabled(tt.args.val)
		})
	}
}

func TestProcSysCommonHandler_SetService(t *testing.T) {
	type fields struct {
		Name      string
		Path      string
		Type      domain.HandlerType
		Enabled   bool
		Cacheable bool
		Service   domain.HandlerServiceIface
	}
	type args struct {
		hs domain.HandlerServiceIface
	}
	tests := []struct {
		name   string
		fields fields
		args   args
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &implementations.ProcSysCommonHandler{
				domain.HandlerBase{
					Name:      tt.fields.Name,
					Path:      tt.fields.Path,
					Type:      tt.fields.Type,
					Enabled:   tt.fields.Enabled,
					Cacheable: tt.fields.Cacheable,
					Service:   tt.fields.Service,
				},
			}
			h.SetService(tt.args.hs)
		})
	}
}

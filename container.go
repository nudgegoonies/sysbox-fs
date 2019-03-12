package main

import (
	"errors"
	"log"
	"strconv"
	"sync"
	"time"
)

//
// File in charge of hosting all the logic dealing with the container-state
// required by Sysvisorfs for its operation.
//

// Global variable holding Sysvisorfs' ContainerStateMap
var ContainerStateMapGlobal *ContainerStateMap

type ContainerStateMap struct {
	sync.RWMutex
	internal map[string]*ContainerState
}

func NewContainerStateMap() *ContainerStateMap {

	cm := &ContainerStateMap{
		internal: make(map[string]*ContainerState),
	}

	return cm
}

func (cm *ContainerStateMap) get(key string) (value *ContainerState, ok bool) {

	cm.RLock()
	res, ok := cm.internal[key]
	cm.RUnlock()

	return res, ok
}

func (cm *ContainerStateMap) set(key string, value *ContainerState) {

	cm.Lock()
	cm.internal[key] = value
	cm.Unlock()
}

func (cm *ContainerStateMap) delete(key string) {

	cm.Lock()
	delete(cm.internal, key)
	cm.Unlock()
}

func (cm *ContainerStateMap) lookup(id string) (*ContainerState, bool) {

	cntr, ok := cm.get(id)
	if !ok {
		return nil, false
	}

	return cntr, true
}

//
// Container type to represent all the container-state relevant to sysvisorfs.
//
type ContainerState struct {
	id         string    // container-id value generated by runC
	initPid    uint32    // initPid within container
	hostname   string    // defined container hostname
	ctime      time.Time // container creation time
	uidFirst   uint32    // first value of Uid range (host side)
	uidSize    uint32    // Uid range size
	gidFirst   uint32    // first value of Gid range (host side)
	gidSize    uint32    // Gid range size
	pidNsInode uint64    // inode associated to container's pid-ns
}

//
// ContainerState constructor
//
func NewContainerState(
	id string,
	initPid uint32,
	hostname string,
	uidFirst uint32,
	uidSize uint32,
	gidFirst uint32,
	gidSize uint32) (*ContainerState, error) {

	//
	// Verify that the new container to create is not already present in
	// the global ContainerMap.
	//
	if _, ok := ContainerStateMapGlobal.get(id); ok {
		return nil, errors.New("Container already registered")
	}

	cntr := &ContainerState{
		id:         id,
		initPid:    initPid,
		hostname:   hostname,
		ctime:      time.Time{}, // initializing ctime with zeroed-timestamp
		uidFirst:   uidFirst,
		uidSize:    uidSize,
		gidFirst:   gidFirst,
		gidSize:    gidSize,
		pidNsInode: 0,
	}

	return cntr, nil
}

//
// Container registration method
//
func (c *ContainerState) register() error {

	//
	// Let's start by identifying the inode corresponding to the pid-namespace
	// associated to this container.
	//
	inode, err := getPidNsInode(c.initPid)
	if err != nil {
		return errors.New("Could not find pid-namespace inode for pid")
	}

	//
	// Verify that the just-found inode is not already present in the global
	// pidContainerMap struct, and if that's not the case update container.
	//
	if _, ok := PidNsContainerMapGlobal.get(inode); ok {
		return errors.New("Pid-namespace already registered")
	}
	c.pidNsInode = inode

	//
	// Insert new container into the global ContainerStateMap. Caller of this
	// method is expected to verify the existence of this container in the
	// ContainerMap through the execution of NewContainer constructor.
	//
	ContainerStateMapGlobal.set(c.id, c)

	//
	// Finalize registration process by inserting the pid-ns-inode into the
	// global pidContainerMap struct.
	//
	PidNsContainerMapGlobal.set(inode, c.id)

	log.Println("Container registration successfully completed:", c.String())

	return nil
}

//
// Container unregistration method
//
func (c *ContainerState) unregister() error {

	//
	// Verify that the pid-ns-inode associated to this container is present
	// in the global PidNsContainerMap, and that its ID fully matches the
	// one of the container to be eliminated.
	//
	cntrId, ok := PidNsContainerMapGlobal.get(c.pidNsInode)
	if !ok || cntrId != c.id {
		return errors.New("Container not properly registered")
	}

	//
	// Let's also verify that the container is present in the global
	// containerMap.
	//
	if _, ok := ContainerStateMapGlobal.get(c.id); !ok {
		return errors.New("Container not properly registered")
	}

	//
	// Proceeding to eliminate all the existing state for this container.
	// Notice that the order is important.
	//
	PidNsContainerMapGlobal.delete(c.pidNsInode)
	ContainerStateMapGlobal.delete(c.id)

	log.Println("Container unregistration successfully completed:", c.String())

	return nil
}

//
// String() specialization for Container type.
//
func (c *ContainerState) String() string {

	return "\n\t\t id: " + c.id + "\n" +
		"\t\t initPid: " + strconv.Itoa(int(c.initPid)) + "\n" +
		"\t\t hostname: " + c.hostname + "\n" +
		"\t\t ctime: " + c.ctime.String() + "\n" +
		"\t\t pidNsInode: " + strconv.FormatUint(c.pidNsInode, 10)
}
